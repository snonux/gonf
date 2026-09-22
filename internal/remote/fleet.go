package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/snonux/gonf/internal/multierr"
)

// DefaultHostTimeout bounds one host's whole push (all chunks: blob upload,
// applies, sticky removal). Applies can legitimately run for minutes, so the
// default is generous; `gonf fleet -host-timeout` overrides it per run. A
// host that outlives its limit is killed and reported as a fleet failure
// instead of holding an errgroup slot forever.
const DefaultHostTimeout = 10 * time.Minute

// hostTimeoutCtx derives the per-host push context: the fleet context bounded
// by hostTimeout when one is configured (hostTimeout <= 0 means unlimited).
func hostTimeoutCtx(fleetCtx context.Context, hostTimeout time.Duration) (context.Context, context.CancelFunc) {
	if hostTimeout > 0 {
		return context.WithTimeout(fleetCtx, hostTimeout)
	}
	return context.WithCancel(fleetCtx)
}

// Group is the set of hosts one fan-out delivers to, and how many at once.
// It is the per-group half of a fan-out; the plan and its Mode travel
// separately in a Delivery, so the same Group shape serves push and preview.
type Group struct {
	// Name labels the summary line and error messages (a cluster name, for
	// both a whole-cluster run and each of a fleet run's per-cluster groups).
	Name string
	// Targets are the resolved SSH destinations; Labels[i] names Targets[i]
	// (its inventory host name) in errors and suffixes its plan ID.
	Targets []PushTarget
	Labels  []string
	// Limit bounds the concurrent per-host deliveries.
	Limit int
	// HostTimeout bounds each host's whole delivery (DefaultHostTimeout for
	// library callers, the CLI -host-timeout flag otherwise; <= 0 means
	// unlimited).
	HostTimeout time.Duration
}

// Fanout delivers one already-recorded plan to every target of g in
// parallel, in d.Mode (Push may bootstrap gonf on a host; Preview never
// does). It is the per-host transport half of the cluster and fleet runs:
// the caller (internal/orchestrate, for api.PushClusterRun and friends)
// resolves the inventory and records the plan once, then hands the resolved
// targets here. The ctx (the CLI signal context for the fan-out) is threaded
// through errgroup.WithContext: SIGINT/SIGTERM kill the in-flight ssh
// sessions, and a failing host cancels its in-flight siblings.
//
// The returned error keeps every per-host cause inspectable: errors.Is /
// errors.As reach a host's plan.Refusal, context.DeadlineExceeded, etc.
// through it (see fanoutTally.err for its shape).
func Fanout(ctx context.Context, d Delivery, g Group) error {
	if err := d.Mode.Validate(); err != nil {
		return err
	}
	tally := &fanoutTally{hostTimeout: g.HostTimeout}
	// WithContext ties the fan-out to ctx (the CLI signal context) and makes
	// a failing host cancel its in-flight siblings: their ssh processes are
	// killed instead of holding errgroup slots forever.
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(g.Limit)
	for i := range g.Targets {
		eg.Go(func() error {
			hostCtx, cancel := hostTimeoutCtx(egCtx, g.HostTimeout)
			defer cancel()
			err := d.forHost(g.Labels[i]).ToHost(hostCtx, g.Targets[i])
			tally.record(g.Labels[i], err)
			// A non-nil return cancels egCtx, aborting the in-flight siblings.
			return err
		})
	}
	// Every goroutine error is collected by tally (failed per host, firstErr
	// for aborts); Wait's own first error would be redundant.
	_ = eg.Wait()

	fmt.Fprintf(os.Stderr, "%s %s (%d ops) to %s (%d/%d hosts)\n",
		d.Mode.Verb(), d.PlanID, len(d.Ops), g.Name, tally.okCount, len(g.Targets))
	return tally.err(g.Name)
}

// JoinGroupErrors aggregates the per-group errors of a multi-group fan-out
// (a fleet run's cluster groups) into one `<prefix>: <err>; <err>` error,
// sorted by message, or nil when errs holds no non-nil error.
//
// It applies the fleet-level half of fanoutTally.err's rule that host
// failures win over an abort. A failing group cancels the shared fleet
// context, so its sibling groups end with `cluster "<name>": aborted: ...
// context canceled`. When at least one group failed for a real reason,
// those aborts are consequences, not causes: they stay in the message (so
// the operator still sees which clusters were cut short) but are flattened
// out of the error chain, so errors.Is(err, context.Canceled) does not
// report a caller cancellation that never happened while the real failures
// stay reachable with errors.Is / errors.As. When every group was aborted
// (the caller's context fired), the aborts are kept as they are and
// context.Canceled stays reachable.
func JoinGroupErrors(prefix string, errs []error) error {
	if !hasNonAbort(errs) {
		return multierr.JoinSorted(prefix, errs)
	}
	members := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil && errors.Is(err, context.Canceled) {
			// Same text, no chain: the abort is reported, not matched.
			err = errors.New(err.Error())
		}
		members = append(members, err)
	}
	return multierr.JoinSorted(prefix, members)
}

// hasNonAbort reports whether errs holds a non-nil error that is not a
// fleet-wide abort (one that does not match context.Canceled).
func hasNonAbort(errs []error) bool {
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return true
		}
	}
	return false
}

// fanoutTally collects the per-host outcomes of one fan-out. record is called
// concurrently by the errgroup goroutines; mu guards every field below it
// while they run. Once eg.Wait has returned, all writes are visible and the
// fields are read directly (fanout's summary line reads okCount that way).
type fanoutTally struct {
	hostTimeout time.Duration

	mu       sync.Mutex
	okCount  int
	failed   []error // per-host failures, each "<label>: <cause>" wrapping the cause
	firstErr error   // first fleet-wide abort (context.Canceled), reported once
}

// record files one host's result. Per-host failures are wrapped with %w (not
// flattened with %v) so the aggregate keeps every cause inspectable with
// errors.Is / errors.As — e.g. a plan.Refusal or context.DeadlineExceeded.
func (t *fanoutTally) record(label string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case err == nil:
		t.okCount++
	case errors.Is(err, context.Canceled):
		// Killed by a fleet-wide abort (a sibling host failed or the CLI
		// context fired), not by this host's own failure: record the abort
		// reason once instead of blaming every in-flight host.
		if t.firstErr == nil {
			t.firstErr = err
		}
	case errors.Is(err, context.DeadlineExceeded):
		t.failed = append(t.failed, fmt.Errorf("%s: %w (host timeout after %s)", label, err, t.hostTimeout))
	default:
		t.failed = append(t.failed, fmt.Errorf("%s: %w", label, err))
	}
}

// err returns the fan-out's aggregate error, or nil when every host
// succeeded. Host failures win over an abort: they are reported as one
// *multierr.Error — `cluster "<name>": h1: ...; h2: ...`, sorted by message,
// the same single line as before — that unwraps to every per-host error.
// With no host failure, an abort is reported once as
// `cluster "<name>": aborted: <cause>`, wrapping the context error. It is
// called after eg.Wait, so the lock only documents the ownership of fields.
func (t *fanoutTally) err(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := multierr.JoinSorted(fmt.Sprintf("cluster %q", name), t.failed); err != nil {
		return err
	}
	if t.firstErr != nil {
		return fmt.Errorf("cluster %q: aborted: %w", name, t.firstErr)
	}
	return nil
}
