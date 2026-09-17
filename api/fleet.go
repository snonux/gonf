package api

import (
	"context"
	"fmt"
	"sort"
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

// HostNames returns unique host inventory names across all member clusters,
// in first-seen registration order.
func (f FleetRef) HostNames() []string {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := fleetsByName[f.name]
	if !ok {
		logger.Fatal("Fleet %q is not registered", f.name)
	}
	seen := map[string]struct{}{}
	var names []string
	for _, c := range rec.clusters {
		crec, ok := clustersByName[c.name]
		if !ok {
			logger.Fatal("Fleet %q: Cluster %q is not registered", f.name, c.name)
		}
		for _, h := range crec.hosts {
			if _, ok := seen[h.name]; ok {
				continue
			}
			seen[h.name] = struct{}{}
			names = append(names, h.name)
		}
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
		seen := map[string]struct{}{}
		var hnames []string
		for i, c := range rec.clusters {
			cnames[i] = c.name
			crec := clustersByName[c.name]
			for _, h := range crec.hosts {
				if _, ok := seen[h.name]; ok {
					continue
				}
				seen[h.name] = struct{}{}
				hnames = append(hnames, h.name)
			}
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

// PushFleetRun is the full-parameter form of PushFleet (CLI threads context,
// planID, -j, and -host-timeout). parallelOverride > 0 overrides the default
// fan-out limit; planID "" → fleet-<name>.
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
	seen := map[string]struct{}{}
	var hosts []HostRef
	for _, c := range rec.clusters {
		crec, ok := clustersByName[c.name]
		if !ok {
			inventoryMu.Unlock()
			return fmt.Errorf("fleet %q: cluster %q is not registered", name, c.name)
		}
		for _, h := range crec.hosts {
			if _, ok := seen[h.name]; ok {
				continue
			}
			seen[h.name] = struct{}{}
			hosts = append(hosts, h)
		}
	}
	inventoryMu.Unlock()

	limit := defaultClusterParallelism
	if parallelOverride > 0 {
		limit = parallelOverride
	}
	if planID == "" {
		planID = "fleet-" + name
	}

	targets := make([]PushTarget, 0, len(hosts))
	labels := make([]string, 0, len(hosts))
	for _, h := range hosts {
		t, err := h.pushTarget()
		if err != nil {
			return err
		}
		targets = append(targets, t)
		labels = append(labels, h.name)
	}

	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := RefuseOpaqueOnlyPush(fmt.Sprintf("fleet %q", name)); err != nil {
		return err
	}
	return remote.Fanout(ctx, name, planID, ops, mem, targets, labels, limit, hostTimeout)
}
