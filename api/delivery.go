package api

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/orchestrate"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// This file shapes every push and strict-preview run behind the exported
// entry points in push.go (PushTo, PreviewTo), cluster.go (PushHost,
// PreviewHost, PushClusterRun, PreviewClusterRun) and fleet.go (PushFleetRun,
// PreviewFleetRun): validating the remote.Mode, choosing labels and default
// plan IDs, recording the plan once as a remote.Delivery, and handing it to
// one target (recordAndPush) or to a cluster or fleet fan-out (groupRun).
// The exported functions only pick the mode, the plan ID and the host
// selection.

// pushOutput overrides every push/preview summary line's destination when a
// test sets it: recordAndPush's single-host line below, and groupRun's
// cluster/fleet line via groupRun.writer/orchestrate.Group.Writer/
// remote.Group.Writer. Left nil (every production run), each layer falls
// back to the CURRENT os.Stderr instead of a value cached here at package
// init — recordAndPush re-reads os.Stderr on every call, and a nil
// groupRun.writer() result reaches remote.Fanout's own os.Stderr fallback —
// so testutil.CaptureStderr's os.Stderr swap still works once this seam
// exists, and every summary line stays byte-identical by default. Tests set
// it (like the remote package's SSHRunner/ensureRuntime seams) to assert on
// the summary text without redirecting the process-wide os.Stderr; tests
// using it must not run in parallel. A future output policy (e.g. 062's
// controller-side secret redaction) wraps this one variable instead of
// touching every call site.
var pushOutput io.Writer

// groupRun is one cluster or fleet run request: the remote.Mode chosen by
// the exported entry point (PushClusterRun vs PreviewClusterRun, PushFleetRun
// vs PreviewFleetRun) plus the per-run parameters they all share, carried as
// one value instead of a strictPreview bool among positional parameters.
type groupRun struct {
	mode             remote.Mode
	name             string
	planID           string // "" → groupPlanID(mode, scope, name)
	parallelOverride int    // > 0 overrides every group's parallelism (-j)
	hostTimeout      time.Duration
	tasks            []string
	// output is this run's Fanout summary destination; nil (every exported
	// entry point today) falls back to pushOutput via writer(). Tests set it
	// directly on a groupRun literal to assert on one run's summary without
	// touching the package-wide seam.
	output io.Writer
}

// writer is r's resolved Fanout destination: r.output when set, else the
// package-wide pushOutput default (see both docs).
func (r groupRun) writer() io.Writer {
	if r.output != nil {
		return r.output
	}
	return pushOutput
}

// recordAndPush validates mode, records tasks with selected as the ForHosts
// host selection (nil → every host), refuses opaque-only plans, and delivers
// the chunks to t in mode: remote.Push may bootstrap gonf on t, remote.Preview
// never does. It is the single implementation behind PushTo/PreviewTo (and
// their *Context forms) and PushHost/PreviewHost. The mode is validated
// first, so an invalid (zero) mode records nothing.
func recordAndPush(ctx context.Context, mode remote.Mode, t PushTarget, planID string, selected []string, tasks ...string) error {
	if err := mode.Validate(); err != nil {
		return err
	}
	label := targetLabel(mode)
	if len(tasks) == 0 {
		return fmt.Errorf("%s: no tasks", label)
	}
	if planID == "" {
		planID = "push"
	}
	d, err := recordDelivery(mode, planID, label, selected, tasks)
	if err != nil {
		return err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remote.DefaultHostTimeout)
		defer cancel()
	}
	if err := d.ToHost(ctx, t); err != nil {
		return err
	}
	// "pushed ... to host" but "previewed ... on host": the wording predates
	// Mode and is kept byte-identical for anyone reading stderr. Writes to
	// pushOutput when a test set it, else the current os.Stderr (read here,
	// not cached, so testutil.CaptureStderr's os.Stderr swap still reaches
	// it); the write's result is discarded like the group fan-out's own
	// summary line (see remote.Fanout), since a failed write to a
	// diagnostic stream must not turn a successful push into an error.
	preposition := "to"
	if mode == remote.Preview {
		preposition = "on"
	}
	w := pushOutput
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintf(w, "%s %s (%d ops) %s %s\n", mode.Verb(), planID, len(d.Ops), preposition, t.Destination())
	return nil
}

// runHost is PushHost/PreviewHost's shared body: resolve h, then record and
// deliver in mode with h's ForHosts host selection.
func runHost(mode remote.Mode, h HostRef, planID string, tasks []string) error {
	t, err := h.pushTarget()
	if err != nil {
		return err
	}
	return recordAndPush(context.Background(), mode, t, planID, inventory.SelectionForHosts([]string{h.name}), tasks...)
}

// recordDelivery records tasks once with selected as the ForHosts host
// selection, refuses an opaque-only plan (label prefixes that refusal), and
// returns the recorded plan as a remote.Delivery in mode. Every push and
// preview entry point (single target, cluster, fleet) records through it, so
// the Delivery — and with it the mode — is built exactly once per run and
// then carried unchanged down to each host. Its callers have already
// validated mode.
func recordDelivery(mode remote.Mode, planID, label string, selected, tasks []string) (remote.Delivery, error) {
	mem := plan.NewMemoryStore()
	ops, err := recordPlanForHosts(selected, planID, mem, tasks...)
	if err != nil {
		return remote.Delivery{}, fmt.Errorf("record: %w", err)
	}
	if err := RefuseOpaqueOnlyPush(label); err != nil {
		return remote.Delivery{}, err
	}
	return remote.Delivery{Mode: mode, PlanID: planID, Ops: ops, Mem: mem}, nil
}

// targetLabel is a single-target run's error prefix: "push" or
// "remote preview". mode must be valid.
func targetLabel(mode remote.Mode) string {
	if mode == remote.Preview {
		return "remote preview"
	}
	return "push"
}

// groupLabel is a cluster or fleet run's error prefix: `cluster "web"` for a
// push, `cluster preview "web"` for a strict preview (scope is "cluster" or
// "fleet"). mode must be valid.
func groupLabel(mode remote.Mode, scope, name string) string {
	if mode == remote.Preview {
		return fmt.Sprintf("%s preview %q", scope, name)
	}
	return fmt.Sprintf("%s %q", scope, name)
}

// groupPlanID is a cluster or fleet run's default plan ID: "cluster-web" for
// a push, "preview-cluster-web" for a strict preview. mode must be valid.
func groupPlanID(mode remote.Mode, scope, name string) string {
	if mode == remote.Preview {
		return "preview-" + scope + "-" + name
	}
	return scope + "-" + name
}

// check rejects an invalid (zero) mode and a run without tasks, before any
// inventory lookup or recording: the labels and default plan ID below only
// distinguish Preview from everything else, so an unchecked zero mode would
// otherwise record the whole plan under a push plan ID.
func (r groupRun) check(scope string) error {
	if err := r.mode.Validate(); err != nil {
		return err
	}
	if len(r.tasks) == 0 {
		return fmt.Errorf("%s: no tasks", groupLabel(r.mode, scope, r.name))
	}
	return nil
}

// record resolves the run's plan ID and records the plan once, with
// selected as the ForHosts host selection (see recordDelivery).
func (r groupRun) record(scope string, selected []string) (remote.Delivery, error) {
	planID := r.planID
	if planID == "" {
		planID = groupPlanID(r.mode, scope, r.name)
	}
	return recordDelivery(r.mode, planID, groupLabel(r.mode, scope, r.name), selected, r.tasks)
}

// limit is one group's concurrency: the -j override when set, otherwise the
// cluster's own configured parallelism.
func (r groupRun) limit(rec inventory.Cluster) int {
	if r.parallelOverride > 0 {
		return r.parallelOverride
	}
	return inventory.ClusterParallelism(rec)
}

// cluster records the run once for the named cluster and delivers it to
// every member host in r.mode.
func (r groupRun) cluster(ctx context.Context) error {
	if err := r.check("cluster"); err != nil {
		return err
	}
	rec, ok := inventory.LookupCluster(r.name)
	if !ok {
		return fmt.Errorf("cluster %q is not registered", r.name)
	}
	// Record once with the cluster's members (plus any inventory name that
	// could match one of them) selected, so ForHosts bodies of hosts the run
	// cannot reach are not resolved.
	d, err := r.record("cluster", inventory.SelectionForHosts(rec.Hosts))
	if err != nil {
		return err
	}
	return orchestrate.Deliver(ctx, d, orchestrate.Group{Name: r.name,
		HostNames: rec.Hosts, Limit: r.limit(rec), HostTimeout: r.hostTimeout, Writer: r.writer()})
}

// fleet records the run once for the named fleet and delivers it, in
// r.mode, to every cluster group; see PushFleetRun for the parallelism and
// cancellation contract.
func (r groupRun) fleet(ctx context.Context) error {
	if err := r.check("fleet"); err != nil {
		return err
	}
	entries, err := inventory.CollectFleetHosts(r.name)
	if err != nil {
		return err
	}
	// Record once with the fleet's deduplicated hosts (plus any inventory
	// name that could match one of them) selected, so ForHosts bodies of
	// hosts the fleet cannot reach are not resolved.
	d, err := r.record("fleet", inventory.SelectionForHosts(fleetHostNames(entries)))
	if err != nil {
		return err
	}
	// One single-line `fleet "<name>": <group err>; <group err>` error with
	// the groups sorted by message; it unwraps to every group's error (and so
	// to every per-host cause) for errors.Is / errors.As, except that when a
	// real group failure exists, the other groups' consequent aborts stay in
	// the text but leave the chain, so the error does not match
	// context.Canceled (see remote.JoinGroupErrors). nil when all succeeded. The prefix stays
	// `fleet "<name>"` in preview mode too.
	errs := r.deliverGroups(ctx, d, inventory.GroupFleetHostsByCluster(entries))
	return remote.JoinGroupErrors(fmt.Sprintf("fleet %q", r.name), errs)
}

// deliverGroups delivers d to every cluster group concurrently and returns
// the groups' errors, unsorted. The errors are kept as values (not flattened
// to strings) so the fleet aggregate still unwraps to every per-host cause
// (remote.JoinGroupErrors only takes consequent aborts out of the chain).
//
// fleetCtx is shared by every group's call: canceling it (the instant any
// group fails) propagates into every OTHER group's errgroup-derived context
// too, restoring the whole-fleet fail-fast contract. Each group still applies
// its own limit independently via its own errgroup.SetLimit inside
// orchestrate.Deliver/remote.Fanout, so this does not undo the per-cluster
// parallelism fix. context.CancelFunc is safe to call concurrently and more
// than once (only the first call has effect), so no extra synchronization
// (e.g. sync.Once) is needed around cancel().
func (r groupRun) deliverGroups(ctx context.Context, d remote.Delivery, groups []inventory.FleetHostGroup) []error {
	fleetCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []error
	for _, g := range groups {
		og := orchestrate.Group{Name: g.Cluster.Name, HostNames: g.HostNames,
			Limit: r.limit(g.Cluster), HostTimeout: r.hostTimeout, Writer: r.writer()}
		wg.Go(func() {
			if err := orchestrate.Deliver(fleetCtx, d, og); err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
				cancel()
			}
		})
	}
	wg.Wait()
	return errs
}
