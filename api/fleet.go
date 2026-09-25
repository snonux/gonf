package api

import (
	"context"
	"time"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/remote"
)

// PushFleet records once and fans out to every unique host across the fleet's
// clusters (default parallelism 5).
func PushFleet(name string, tasks ...string) error {
	return PushFleetRun(context.Background(), name, "", 0, remote.DefaultHostTimeout, tasks...)
}

// PushFleetRun is the full-parameter form of PushFleet (CLI threads context,
// planID, -j, and -host-timeout). planID "" → fleet-<name>.
//
// Fleet parallelism semantics (the decision this replaces a real bug with):
// a fleet push groups the fleet's deduplicated hosts by the cluster that
// first claims them (member clusters may share hosts; each host is still
// pushed exactly once, via the cluster that first lists it — see
// inventory.CollectFleetHosts), then pushes each group at THAT CLUSTER's OWN
// configured Parallel(n), not a blanket fleet-wide default. This makes
// Cluster.Parallel(n) mean the same thing everywhere it is honored, whether
// the cluster is reached directly (`gonf cluster`) or indirectly through a
// fleet (`gonf fleet`) — a cluster configured with e.g. Parallel(2) because
// its hosts are fragile/rate-limited must not suddenly be pushed at a
// higher, unrelated concurrency just because it was reached via a fleet.
// Earlier code always used defaultClusterParallelism for the entire fleet
// fan-out, silently discarding every member cluster's own Parallel(n); see
// docs/design/plan.md "Fleet parallelism semantics" and the regression test
// TestPushFleetHonorsClusterParallel (api/cluster_test.go), which fails
// against that earlier behavior.
//
// parallelOverride > 0 (the CLI's `-j`) is an explicit, per-run request and
// overrides every group's limit uniformly — it wins over both the
// per-cluster setting and the fleet-wide default.
//
// Each cluster's group still gets its own remote.Fanout call (so its own
// Parallel(n)/-j limit governs only that group's concurrency), but all
// groups share one cancelable context derived from ctx: as soon as any
// group's orchestrate.Deliver call returns an error, PushFleetRun cancels
// that shared context, which propagates into every other group's
// still-running errgroup (each group's errgroup.WithContext derives from the
// shared context, not from ctx directly) and aborts their in-flight — and
// not-yet-started — hosts too. This restores the pre-existing whole-fleet
// fail-fast contract ("a failing host cancels its in-flight siblings ... the
// fleet error reports the abort reason once", docs/design/plan.md "Timeouts and
// cancellation") on top of the per-cluster parallelism fix: parallelism is
// per-cluster, but failure cancellation is whole-fleet. See docs/design/plan.md
// "Fleet parallelism semantics" and TestPushFleetFailureCancelsOtherClusters
// (api/cluster_test.go).
func PushFleetRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	return groupRun{mode: remote.Push, name: name, planID: planID,
		parallelOverride: parallelOverride, hostTimeout: hostTimeout, tasks: tasks}.fleet(ctx)
}

// PreviewFleetRun records tasks once and performs strict non-mutating remote
// previews across every host in a fleet (planID "" → preview-fleet-<name>),
// with the same parallelism and cancellation contract as PushFleetRun.
// Missing or stale remote gonf binaries fail instead of being installed or
// updated.
func PreviewFleetRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	return groupRun{mode: remote.Preview, name: name, planID: planID,
		parallelOverride: parallelOverride, hostTimeout: hostTimeout, tasks: tasks}.fleet(ctx)
}

// fleetHostNames returns the inventory host names of a fleet's collected
// entries, in first-seen order. It backs FleetRef.HostNames and is the
// record-time ForHosts host selection of a fleet push; it is never nil, so a
// fleet push always records with an explicit selection.
func fleetHostNames(entries []inventory.FleetHostEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.HostName
	}
	return names
}
