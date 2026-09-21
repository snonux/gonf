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
	"github.com/snonux/gonf/plan"
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

// Fanout pushes one already-recorded plan to every target in parallel. It is
// the per-host transport half of the fleet push: the caller (api.PushClusterRun)
// resolves the fleet inventory and records the plan once, then hands the
// resolved targets here. name labels the summary line and error messages;
// labels[i] names targets[i]. The ctx (the CLI signal context for the fleet
// fan-out) is thread through errgroup.WithContext: SIGINT/SIGTERM kill the
// in-flight ssh pushes, and a failing host cancels its in-flight siblings.
// Each host's push is bounded by hostTimeout (DefaultHostTimeout for library
// callers, the CLI -host-timeout flag otherwise; <= 0 means unlimited).
//
// The returned error keeps every per-host cause inspectable: errors.Is /
// errors.As reach a host's plan.Refusal, context.DeadlineExceeded, etc.
// through it (see fanoutTally.err for its shape).
func Fanout(ctx context.Context, name, planID string, ops []plan.Op, mem plan.BlobReader, targets []PushTarget, labels []string, limit int, hostTimeout time.Duration) error {
	return fanout(ctx, name, planID, ops, mem, targets, labels, limit, hostTimeout, false)
}

// PreviewFanout is Fanout's strict-preview variant. Each target must already
// have a compatible gonf runtime; unlike Fanout it never bootstraps it.
func PreviewFanout(ctx context.Context, name, planID string, ops []plan.Op, mem plan.BlobReader, targets []PushTarget, labels []string, limit int, hostTimeout time.Duration) error {
	return fanout(ctx, name, planID, ops, mem, targets, labels, limit, hostTimeout, true)
}

// fanout runs one PushChunks (or PreviewChunks) per target under an errgroup
// bounded by limit, prints the summary line and returns the aggregate error.
func fanout(ctx context.Context, name, planID string, ops []plan.Op, mem plan.BlobReader, targets []PushTarget, labels []string, limit int, hostTimeout time.Duration, strictPreview bool) error {
	tally := &fanoutTally{hostTimeout: hostTimeout}
	// WithContext ties the fan-out to ctx (the CLI signal context) and makes
	// a failing host cancel its in-flight siblings: their ssh processes are
	// killed instead of holding errgroup slots forever.
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(limit)
	for i := range targets {
		eg.Go(func() error {
			hostCtx, cancel := hostTimeoutCtx(egCtx, hostTimeout)
			defer cancel()
			var err error
			if strictPreview {
				err = PreviewChunks(hostCtx, targets[i], planID+"-"+labels[i], ops, mem)
			} else {
				err = PushChunks(hostCtx, targets[i], planID+"-"+labels[i], ops, mem)
			}
			tally.record(labels[i], err)
			// A non-nil return cancels egCtx, aborting the in-flight siblings.
			return err
		})
	}
	// Every goroutine error is collected by tally (failed per host, firstErr
	// for aborts); Wait's own first error would be redundant.
	_ = eg.Wait()

	verb := "pushed"
	if strictPreview {
		verb = "previewed"
	}
	fmt.Fprintf(os.Stderr, "%s %s (%d ops) to %s (%d/%d hosts)\n",
		verb, planID, len(ops), name, tally.okCount, len(targets))
	return tally.err(name)
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
