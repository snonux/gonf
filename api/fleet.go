package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// FleetRef is an opaque handle for a named set of clusters.
// Construct with Fleet(...ClusterRef); look up with LookupFleet / MustFleet.
type FleetRef struct {
	name string
}

type fleetOfClustersRecord struct {
	name     string
	clusters []ClusterRef
}

// FleetInfo is a listing row for registered fleets (lists of clusters).
type FleetInfo struct {
	Name     string
	Clusters []string
	Hosts    []string // flattened unique hosts across member clusters
}

var fleetsByName = map[string]fleetOfClustersRecord{}

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

	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	if _, ok := fleetsByName[name]; ok {
		logger.Fatal("Fleet %q already registered", name)
	}
	for _, c := range clusters {
		if _, ok := clustersByName[c.name]; !ok {
			logger.Fatal("Fleet %q: Cluster %q is not registered", name, c.name)
		}
	}
	fleetsByName[name] = fleetOfClustersRecord{
		name:     name,
		clusters: append([]ClusterRef(nil), clusters...),
	}
	return FleetRef{name: name}
}

// LookupFleet returns a registered FleetRef.
func LookupFleet(name string) (FleetRef, bool) {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	if _, ok := fleetsByName[name]; !ok {
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
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := fleetsByName[f.name]
	if !ok {
		logger.Fatal("Fleet %q is not registered", f.name)
	}
	names := make([]string, len(rec.clusters))
	for i, c := range rec.clusters {
		names[i] = c.name
	}
	return names
}

// fleetHostEntry pairs a HostRef with the clusterRecord that first claims it
// within a fleet — when member clusters share a host, the first cluster to
// list it (in Fleet(...) registration order) "owns" it for dedup purposes.
// PushFleetRun groups entries by this owning cluster so each host group can
// be pushed at THAT cluster's own configured Parallel(n) rather than a
// blanket fleet-wide default — see PushFleetRun's doc comment and
// docs/plan.md "Fleet parallelism semantics".
type fleetHostEntry struct {
	host    HostRef
	cluster clusterRecord
}

// collectFleetHosts walks rec's member clusters and returns each unique
// host exactly once, in first-seen registration order, paired with the
// cluster that first claims it. It is the single unique-hosts-collection
// implementation shared by FleetRef.HostNames(), Fleets(), and
// PushFleetRun, which previously each carried their own copy of this loop.
// Caller must hold inventoryMu.
func collectFleetHosts(fleetName string, rec fleetOfClustersRecord) ([]fleetHostEntry, error) {
	seen := map[string]struct{}{}
	var out []fleetHostEntry
	for _, c := range rec.clusters {
		crec, ok := clustersByName[c.name]
		if !ok {
			return nil, fmt.Errorf("Fleet %q: Cluster %q is not registered", fleetName, c.name)
		}
		for _, h := range crec.hosts {
			if _, dup := seen[h.name]; dup {
				continue
			}
			seen[h.name] = struct{}{}
			out = append(out, fleetHostEntry{host: h, cluster: crec})
		}
	}
	return out, nil
}

// HostNames returns unique host inventory names across all member clusters,
// in first-seen registration order.
func (f FleetRef) HostNames() []string {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := fleetsByName[f.name]
	if !ok {
		logger.Fatal("Fleet %q is not registered", f.name)
	}
	entries, err := collectFleetHosts(f.name, rec)
	if err != nil {
		logger.Fatal("%v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.host.name
	}
	return names
}

// Fleets lists registered fleets sorted by name.
func Fleets() []FleetInfo {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	out := make([]FleetInfo, 0, len(fleetsByName))
	for _, rec := range fleetsByName {
		cnames := make([]string, len(rec.clusters))
		for i, c := range rec.clusters {
			cnames[i] = c.name
		}
		// Membership is enforced at Fleet/Cluster registration time and the
		// registry is only ever cleared wholesale (ResetInventory), so a
		// missing member cluster here cannot happen in practice; ignore the
		// error rather than panicking a listing call over it.
		entries, _ := collectFleetHosts(rec.name, rec)
		hnames := make([]string, len(entries))
		for i, e := range entries {
			hnames[i] = e.host.name
		}
		out = append(out, FleetInfo{Name: rec.name, Clusters: cnames, Hosts: hnames})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PushFleet records once and fans out to every unique host across the fleet's
// clusters (default parallelism 5).
func PushFleet(name string, tasks ...string) error {
	return PushFleetRun(context.Background(), name, "", 0, remote.DefaultHostTimeout, tasks...)
}

// fleetHostGroup is one member cluster's contribution to a fleet push: the
// (deduplicated) hosts owned by that cluster, to be pushed at the cluster's
// own configured Parallel(n) — see PushFleetRun.
type fleetHostGroup struct {
	cluster clusterRecord
	hosts   []HostRef
}

// groupFleetHostsByCluster buckets entries (already deduplicated by
// collectFleetHosts, one entry per unique host) by owning cluster,
// preserving first-seen cluster order.
func groupFleetHostsByCluster(entries []fleetHostEntry) []fleetHostGroup {
	order := make([]string, 0, len(entries))
	byCluster := make(map[string]*fleetHostGroup, len(entries))
	for _, e := range entries {
		g, ok := byCluster[e.cluster.name]
		if !ok {
			g = &fleetHostGroup{cluster: e.cluster}
			byCluster[e.cluster.name] = g
			order = append(order, e.cluster.name)
		}
		g.hosts = append(g.hosts, e.host)
	}
	out := make([]fleetHostGroup, len(order))
	for i, name := range order {
		out[i] = *byCluster[name]
	}
	return out
}

// PushFleetRun is the full-parameter form of PushFleet (CLI threads context,
// planID, -j, and -host-timeout). planID "" → fleet-<name>.
//
// Fleet parallelism semantics (the decision this replaces a real bug with):
// a fleet push groups the fleet's deduplicated hosts by the cluster that
// first claims them (member clusters may share hosts; each host is still
// pushed exactly once, via the cluster that first lists it — see
// collectFleetHosts), then pushes each group at THAT CLUSTER's OWN
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
// Each cluster's group is still pushed with remote.Fanout's own
// fail-cancels-siblings behavior (a failing host aborts its in-flight
// cluster-mates), but groups run as independent fan-outs: a failure in one
// member cluster does not abort another member cluster's in-flight hosts.
// Narrowing the abort blast radius from "whole fleet" to "one cluster" is a
// direct consequence of giving each cluster its own bounded fan-out, and is
// arguably more correct — an unrelated, healthy cluster should not be
// killed because a different, possibly-fragile cluster failed.
func PushFleetRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("fleet %q: no tasks", name)
	}
	inventoryMu.Lock()
	rec, ok := fleetsByName[name]
	if !ok {
		inventoryMu.Unlock()
		return fmt.Errorf("fleet %q is not registered", name)
	}
	entries, err := collectFleetHosts(name, rec)
	inventoryMu.Unlock()
	if err != nil {
		return err
	}

	if planID == "" {
		planID = "fleet-" + name
	}
	groups := groupFleetHostsByCluster(entries)

	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := RefuseOpaqueOnlyPush(fmt.Sprintf("fleet %q", name)); err != nil {
		return err
	}

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []string
	for _, g := range groups {
		limit := clusterParallelism(g.cluster)
		if parallelOverride > 0 {
			limit = parallelOverride
		}
		wg.Add(1)
		go func(g fleetHostGroup, limit int) {
			defer wg.Done()
			if err := pushHosts(ctx, g.cluster.name, planID, g.hosts, limit, hostTimeout, ops, mem); err != nil {
				errMu.Lock()
				errs = append(errs, err.Error())
				errMu.Unlock()
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
