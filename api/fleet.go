package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/orchestrate"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// FleetRef is an opaque handle for a named set of clusters.
// Construct with Fleet(...ClusterRef); look up with LookupFleet / MustFleet.
type FleetRef struct {
	name string
}

// FleetInfo is a listing row for registered fleets (lists of clusters).
type FleetInfo struct {
	Name     string
	Clusters []string
	Hosts    []string // flattened unique hosts across member clusters
}

// Fleet registers a named set of ClusterRef handles. Each cluster may appear
// at most once. Hosts may overlap across clusters; PushFleet deduplicates.
// Registration-time misuse fails fast via logger.Fatal.
func Fleet(name string, clusters ...ClusterRef) FleetRef {
	if name == "" {
		logger.Fatal("Fleet: name must not be empty")
	}
	if len(clusters) == 0 {
		logger.Fatal("Fleet %q: must include at least one Cluster", name)
	}
	seen := map[string]struct{}{}
	for _, c := range clusters {
		if c.name == "" {
			logger.Fatal("Fleet %q: invalid empty Cluster handle", name)
		}
		if _, ok := seen[c.name]; ok {
			logger.Fatal("Fleet %q: duplicate Cluster %q", name, c.name)
		}
		seen[c.name] = struct{}{}
	}

	names := make([]string, len(clusters))
	for i, c := range clusters {
		names[i] = c.name
	}
	inventory.AddFleet(name, names)
	return FleetRef{name: name}
}

// LookupFleet returns a registered FleetRef.
func LookupFleet(name string) (FleetRef, bool) {
	if _, ok := inventory.LookupFleet(name); !ok {
		return FleetRef{}, false
	}
	return FleetRef{name: name}, true
}

// MustFleet returns LookupFleet or logger.Fatal.
func MustFleet(name string) FleetRef {
	f, ok := LookupFleet(name)
	if !ok {
		logger.Fatal("Fleet %q is not registered", name)
	}
	return f
}

// ClusterNames returns member cluster names in registration order.
func (f FleetRef) ClusterNames() []string {
	rec, ok := inventory.LookupFleet(f.name)
	if !ok {
		logger.Fatal("Fleet %q is not registered", f.name)
	}
	return append([]string(nil), rec.Clusters...)
}

// HostNames returns unique host inventory names across all member clusters,
// in first-seen registration order.
func (f FleetRef) HostNames() []string {
	entries, err := inventory.CollectFleetHosts(f.name)
	if err != nil {
		logger.Fatal("%v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.HostName
	}
	return names
}

// Fleets lists registered fleets sorted by name.
func Fleets() []FleetInfo {
	recs := inventory.FleetInfos()
	out := make([]FleetInfo, 0, len(recs))
	for _, rec := range recs {
		out = append(out, FleetInfo{
			Name:     rec.Name,
			Clusters: append([]string(nil), rec.Clusters...),
			Hosts:    rec.Hosts,
		})
	}
	return out
}

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
// docs/plan.md "Fleet parallelism semantics" and the regression test
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
// group's orchestrate.Push call returns an error, PushFleetRun cancels that shared
// context, which propagates into every other group's still-running
// errgroup (each group's errgroup.WithContext derives from the shared
// context, not from ctx directly) and aborts their in-flight — and
// not-yet-started — hosts too. This restores the pre-existing whole-fleet
// fail-fast contract ("a failing host cancels its in-flight siblings ... the
// fleet error reports the abort reason once", docs/plan.md "Timeouts and
// cancellation") on top of the per-cluster parallelism fix: parallelism is
// per-cluster, but failure cancellation is whole-fleet. See docs/plan.md
// "Fleet parallelism semantics" and TestPushFleetFailureCancelsOtherClusters
// (api/cluster_test.go).
func PushFleetRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("fleet %q: no tasks", name)
	}
	entries, err := inventory.CollectFleetHosts(name)
	if err != nil {
		return err
	}

	if planID == "" {
		planID = "fleet-" + name
	}
	groups := inventory.GroupFleetHostsByCluster(entries)

	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := RefuseOpaqueOnlyPush(fmt.Sprintf("fleet %q", name)); err != nil {
		return err
	}

	// fleetCtx is shared by every group's push call: canceling it (below, the
	// instant any group fails) propagates into every OTHER group's
	// errgroup-derived context too, restoring the whole-fleet fail-fast
	// contract. Each group still applies its own limit independently via its
	// own errgroup.SetLimit inside orchestrate.Push/remote.Fanout, so this
	// does not undo the per-cluster parallelism fix. context.CancelFunc is
	// safe to call concurrently and more than once (only the first call has
	// effect), so no extra synchronization (e.g. sync.Once) is needed around
	// cancel().
	fleetCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []string
	for _, g := range groups {
		limit := inventory.ClusterParallelism(g.Cluster)
		if parallelOverride > 0 {
			limit = parallelOverride
		}
		wg.Add(1)
		go func(g inventory.FleetHostGroup, limit int) {
			defer wg.Done()
			if err := orchestrate.Push(fleetCtx, g.Cluster.Name, planID, g.HostNames, limit, hostTimeout, ops, mem); err != nil {
				errMu.Lock()
				errs = append(errs, err.Error())
				errMu.Unlock()
				cancel()
			}
		}(g, limit)
	}
	wg.Wait()

	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("fleet %q: %s", name, strings.Join(errs, "; "))
	}
	return nil
}
