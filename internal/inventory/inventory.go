// Package inventory owns the host/cluster/fleet registry that backs the
// public api package's Host/Cluster/Fleet DSL. It holds the actual record
// data and the mutex guarding it; api.HostRef/ClusterRef/FleetRef remain
// thin opaque name-handles defined in api, which call into this package for
// every read or write of the registry.
//
// This is a one-way dependency (api -> internal/inventory): this package
// must never import api. It does import internal/remote to build
// remote.PushTarget directly from a registered host's data (PushTargetFor),
// since remote.PushTarget already carries the exact fields a push needs and
// api's own PushTarget is a type alias for it.
package inventory

import (
	"fmt"
	"sort"
	"sync"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
)

// DefaultClusterParallelism is the fan-out width a cluster uses when it has
// not called Parallel(n).
const DefaultClusterParallelism = 5

// Host is one registered SSH destination's stored data.
type Host struct {
	Name      string
	User      string
	SSHHost   string
	Port      int
	Identity  string
	Privilege privilege.Mode
	Values    map[string]any // arbitrary per-host recipe values (WithValue / SetValue)
	GOOS      string
	GOARCH    string
	GonfPath  string
}

// HostOption configures a Host at registration (mirrors api.HostOption).
type HostOption func(*Host)

// Cluster is a registered named set of hosts.
type Cluster struct {
	Name        string
	Hosts       []string // member host names, registration order
	Parallelism int      // 0 -> DefaultClusterParallelism; <0 -> all hosts at once
}

// Fleet is a registered named set of clusters.
type Fleet struct {
	Name     string
	Clusters []string // member cluster names, registration order
}

var (
	mu       sync.Mutex
	hosts    = map[string]Host{}
	clusters = map[string]Cluster{}
	fleets   = map[string]Fleet{}
)

// AddHost registers name with opts applied and returns the stored record.
// Registration-time misuse (empty name, duplicate) fails fast via
// logger.Fatal, matching the rest of the inventory DSL's fail-fast contract.
func AddHost(name string, opts ...HostOption) Host {
	if name == "" {
		logger.Fatal("Host: name must not be empty")
	}
	rec := Host{Name: name, SSHHost: name}
	for _, o := range opts {
		o(&rec)
	}
	if rec.SSHHost == "" {
		rec.SSHHost = name
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := hosts[name]; ok {
		logger.Fatal("Host %q already registered", name)
	}
	hosts[name] = rec
	return rec
}

// SetHostValue stores value under key on an already-registered host (same
// rules as a HostOption WithValue: empty key or duplicate key fails fast).
func SetHostValue(name, key string, value any) {
	if key == "" {
		logger.Fatal("SetValue: key must not be empty")
	}
	mu.Lock()
	defer mu.Unlock()
	rec, ok := hosts[name]
	if !ok {
		logger.Fatal("SetValue: Host %q is not registered", name)
	}
	if rec.Values == nil {
		rec.Values = map[string]any{}
	}
	if _, exists := rec.Values[key]; exists {
		logger.Fatal("Host %q: value key %q already set", name, key)
	}
	rec.Values[key] = value
	hosts[name] = rec
}

// LookupHost returns the registered Host record for name.
func LookupHost(name string) (Host, bool) {
	mu.Lock()
	defer mu.Unlock()
	rec, ok := hosts[name]
	return rec, ok
}

// AddCluster registers name with the given member host names. hostNames is
// assumed already validated for emptiness/uniqueness by the caller (api's
// checkClusterHostsUnique runs before this, over the caller's own HostRef
// handles); AddCluster re-validates registration invariants that only this
// package's state can answer (every host must already be registered).
func AddCluster(name string, hostNames []string) Cluster {
	if name == "" {
		logger.Fatal("Cluster: name must not be empty")
	}
	if len(hostNames) == 0 {
		logger.Fatal("Cluster %q: must include at least one Host", name)
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := clusters[name]; ok {
		logger.Fatal("Cluster %q already registered", name)
	}
	for _, h := range hostNames {
		if _, ok := hosts[h]; !ok {
			logger.Fatal("Cluster %q: Host %q is not registered", name, h)
		}
	}
	rec := Cluster{Name: name, Hosts: append([]string(nil), hostNames...)}
	clusters[name] = rec
	return rec
}

// SetClusterParallel sets cluster name's fan-out width. n < 1 means all
// hosts at once. Fatal if the cluster is not registered.
func SetClusterParallel(name string, n int) {
	mu.Lock()
	defer mu.Unlock()
	rec, ok := clusters[name]
	if !ok {
		logger.Fatal("Cluster %q is not registered", name)
	}
	if n < 1 {
		rec.Parallelism = -1
	} else {
		rec.Parallelism = n
	}
	clusters[name] = rec
}

// LookupCluster returns the registered Cluster record for name.
func LookupCluster(name string) (Cluster, bool) {
	mu.Lock()
	defer mu.Unlock()
	rec, ok := clusters[name]
	return rec, ok
}

// ClusterParallelism resolves c's effective fan-out width.
func ClusterParallelism(c Cluster) int {
	switch {
	case c.Parallelism < 0:
		if n := len(c.Hosts); n > 0 {
			return n
		}
		return 1
	case c.Parallelism == 0:
		return DefaultClusterParallelism
	default:
		return c.Parallelism
	}
}

// AddFleet registers name with the given member cluster names.
func AddFleet(name string, clusterNames []string) Fleet {
	if name == "" {
		logger.Fatal("Fleet: name must not be empty")
	}
	if len(clusterNames) == 0 {
		logger.Fatal("Fleet %q: must include at least one Cluster", name)
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := fleets[name]; ok {
		logger.Fatal("Fleet %q already registered", name)
	}
	for _, c := range clusterNames {
		if _, ok := clusters[c]; !ok {
			logger.Fatal("Fleet %q: Cluster %q is not registered", name, c)
		}
	}
	rec := Fleet{Name: name, Clusters: append([]string(nil), clusterNames...)}
	fleets[name] = rec
	return rec
}

// LookupFleet returns the registered Fleet record for name.
func LookupFleet(name string) (Fleet, bool) {
	mu.Lock()
	defer mu.Unlock()
	rec, ok := fleets[name]
	return rec, ok
}

// HostInfos lists registered hosts sorted by name.
func HostInfos() []Host {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Host, 0, len(hosts))
	for _, rec := range hosts {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ClusterInfos lists registered clusters sorted by name.
func ClusterInfos() []Cluster {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Cluster, 0, len(clusters))
	for _, rec := range clusters {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FleetHostEntry pairs a host name with the Cluster that first claims it
// within a fleet — when member clusters share a host, the first cluster to
// list it (in Fleet registration order) "owns" it for dedup purposes. See
// CollectFleetHosts and GroupFleetHostsByCluster.
type FleetHostEntry struct {
	HostName string
	Cluster  Cluster
}

// CollectFleetHosts walks fleetName's member clusters and returns each
// unique host exactly once, in first-seen registration order, paired with
// the cluster that first claims it. It is the single unique-hosts-collection
// implementation shared by every fleet host listing/push path.
func CollectFleetHosts(fleetName string) ([]FleetHostEntry, error) {
	mu.Lock()
	defer mu.Unlock()
	rec, ok := fleets[fleetName]
	if !ok {
		return nil, fmt.Errorf("Fleet %q is not registered", fleetName)
	}
	return collectFleetHostsLocked(fleetName, rec)
}

// collectFleetHostsLocked is CollectFleetHosts' body; callers must hold mu.
func collectFleetHostsLocked(fleetName string, rec Fleet) ([]FleetHostEntry, error) {
	seen := map[string]struct{}{}
	var out []FleetHostEntry
	for _, cname := range rec.Clusters {
		crec, ok := clusters[cname]
		if !ok {
			return nil, fmt.Errorf("Fleet %q: Cluster %q is not registered", fleetName, cname)
		}
		for _, h := range crec.Hosts {
			if _, dup := seen[h]; dup {
				continue
			}
			seen[h] = struct{}{}
			out = append(out, FleetHostEntry{HostName: h, Cluster: crec})
		}
	}
	return out, nil
}

// FleetInfo is a listing row for a registered fleet: its data plus the
// deduplicated host names across its member clusters.
type FleetInfo struct {
	Fleet
	Hosts []string
}

// FleetInfos lists registered fleets sorted by name.
func FleetInfos() []FleetInfo {
	mu.Lock()
	defer mu.Unlock()
	out := make([]FleetInfo, 0, len(fleets))
	for _, rec := range fleets {
		// Membership is enforced at Fleet/Cluster registration time and the
		// registry is only ever cleared wholesale (Reset), so a missing
		// member cluster here cannot happen in practice; ignore the error
		// rather than panicking a listing call over it.
		entries, _ := collectFleetHostsLocked(rec.Name, rec)
		hnames := make([]string, len(entries))
		for i, e := range entries {
			hnames[i] = e.HostName
		}
		out = append(out, FleetInfo{Fleet: rec, Hosts: hnames})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FleetHostGroup is one member cluster's contribution to a fleet push: the
// (deduplicated) hosts owned by that cluster, to be pushed at the cluster's
// own configured Parallel(n).
type FleetHostGroup struct {
	Cluster   Cluster
	HostNames []string
}

// GroupFleetHostsByCluster buckets entries (already deduplicated by
// CollectFleetHosts, one entry per unique host) by owning cluster,
// preserving first-seen cluster order.
func GroupFleetHostsByCluster(entries []FleetHostEntry) []FleetHostGroup {
	order := make([]string, 0, len(entries))
	byCluster := make(map[string]*FleetHostGroup, len(entries))
	for _, e := range entries {
		g, ok := byCluster[e.Cluster.Name]
		if !ok {
			g = &FleetHostGroup{Cluster: e.Cluster}
			byCluster[e.Cluster.Name] = g
			order = append(order, e.Cluster.Name)
		}
		g.HostNames = append(g.HostNames, e.HostName)
	}
	out := make([]FleetHostGroup, len(order))
	for i, name := range order {
		out[i] = *byCluster[name]
	}
	return out
}

// PushTargetFor builds the remote.PushTarget for a registered host.
func PushTargetFor(name string) (remote.PushTarget, error) {
	mu.Lock()
	defer mu.Unlock()
	rec, ok := hosts[name]
	if !ok {
		return remote.PushTarget{}, fmt.Errorf("host %q is not registered", name)
	}
	return remote.PushTarget{
		User:      rec.User,
		Host:      rec.SSHHost,
		Port:      rec.Port,
		Identity:  rec.Identity,
		Privilege: rec.Privilege,
		GOOS:      rec.GOOS,
		GOARCH:    rec.GOARCH,
		GonfPath:  rec.GonfPath,
	}, nil
}

// Reset clears all registries (tests / api.ResetInventory).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	hosts = map[string]Host{}
	clusters = map[string]Cluster{}
	fleets = map[string]Fleet{}
}
