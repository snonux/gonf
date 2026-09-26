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
	"reflect"
	"sort"
	"sync"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
)

// DefaultClusterParallelism is the fan-out width a cluster uses when it has
// not called Parallel(n).
const DefaultClusterParallelism = 5

// Host is one registered SSH destination's stored data. It is a pure record:
// the value LookupHost/HostInfos return and that gets copied into the hosts
// map has no transient validation-error channel and no way to reach one —
// registration-time misuse is threaded through HostOption's own return value
// instead (see AddHost), never stashed on a Host field. The one transient
// field, defaults (which WithValue/WithData keys a HostDefaults bundle set,
// so a later option may override them), lives only while AddHost applies
// options and is cleared before the record is stored.
type Host struct {
	Name      string
	User      string
	SSHHost   string
	Port      int
	Identity  string
	Privilege privilege.Mode
	Values    map[string]any // arbitrary per-host recipe values (WithValue / SetValue)
	// Data holds typed per-host recipe values (WithData), keyed by each
	// value's concrete type, so a recipe reads them by type (api.EachHost)
	// instead of by a string key.
	Data     map[reflect.Type]any
	GOOS     string
	GOARCH   string
	GonfPath string
	// HostnameMatch is the hostname fragment OnCluster, EachHost and
	// ForHosts guard this host's work on (hostname_contains, case
	// insensitive). Empty means the inventory name (HostnameMatchFor).
	HostnameMatch string
	// PlanRecipient is this host's age1pq recipient for `gonf plan -seal
	// -for` (task 4b2, w82 phase 2, docs/design/plan-encryption.md "Keys"): the
	// destination decrypts a plan sealed to it with the matching identity
	// (its own /etc/gonf/identity, see docs/design/plan-encryption.md "Runbook").
	// Set by api.WithPlanRecipient, which validates it with
	// plan/seal.ParseRecipients before storing it here, so every non-empty
	// value is already a well-formed age1pq hybrid recipient. Empty means
	// the host has none, which `-for` refuses to seal for (naming the
	// host) rather than silently skipping it.
	PlanRecipient string

	// defaults is option-application scratch state (see hostDefaults), set
	// only while AddHost applies options and always nil in a stored record.
	defaults *hostDefaults
}

// HostOption configures a Host at registration (mirrors api.HostOption). An
// option that finds its arguments invalid returns its own error instead of
// setting fields; AddHost collects the first one reported.
type HostOption func(*Host) error

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

// hosts/clusters/fleets are deliberately package-level global state, unlike
// the exec/network runner seams in internal/remote (SSHRunner, SCPRunner,
// GoBuildRunner, Pusher's fields): this is the DSL registry a user's gonf
// config populates by calling api.Host(...)/api.Cluster(...)/api.Fleet(...)
// at plain top-level init-time, before any controller/CLI wiring exists to
// inject a struct into. There is exactly one registry per process, by
// design — a gonf config IS the process's inventory, the same way a Go
// program's init()-registered flag set or sql driver registry is process-
// wide — and every read/write already goes through the mu mutex below, so
// concurrent registration and lookup are safe. This is not an oversight left
// over from the runner-seam refactor; it is the intentional DSL/config half
// of the codebase, left alone on purpose (see task p5's annotations).
var (
	mu       sync.Mutex
	hosts    = map[string]Host{}
	clusters = map[string]Cluster{}
	fleets   = map[string]Fleet{}
)

// AddHost registers name with opts applied and returns the stored record.
// Registration-time misuse (empty name, a HostOption that rejected its
// arguments, a duplicate) is returned as an error and nothing is registered;
// api reports it as a declaration error (internal/declerr). Every option runs
// (later ones may still mutate rec), but only the first error is kept, since
// a later one is usually a consequence of it.
func AddHost(name string, opts ...HostOption) (Host, error) {
	if name == "" {
		return Host{}, fmt.Errorf("Host: name must not be empty")
	}
	rec := Host{Name: name, SSHHost: name}
	var optErr error
	for _, o := range opts {
		if err := o(&rec); err != nil && optErr == nil {
			optErr = err
		}
	}
	rec.defaults = nil // the stored record carries no option scratch state
	if optErr != nil {
		return Host{}, optErr
	}
	if rec.SSHHost == "" {
		rec.SSHHost = name
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := hosts[name]; ok {
		return Host{}, fmt.Errorf("Host %q already registered", name)
	}
	hosts[name] = rec
	return rec, nil
}

// HostnameMatchFor returns the hostname fragment that selects host name on
// a destination: its WithHostnameMatch, else the name itself (also for an
// unregistered name).
func HostnameMatchFor(name string) string {
	mu.Lock()
	defer mu.Unlock()
	if rec, ok := hosts[name]; ok && rec.HostnameMatch != "" {
		return rec.HostnameMatch
	}
	return name
}

// SetHostValue stores value under key on an already-registered host (same
// rules as a HostOption WithValue). An empty key, an unknown host or a key
// already set is returned as an error and nothing is stored.
func SetHostValue(name, key string, value any) error {
	if key == "" {
		return fmt.Errorf("SetValue: key must not be empty")
	}
	mu.Lock()
	defer mu.Unlock()
	rec, ok := hosts[name]
	if !ok {
		return fmt.Errorf("SetValue: Host %q is not registered", name)
	}
	if _, exists := rec.Values[key]; exists {
		return fmt.Errorf("Host %q: value key %q already set", name, key)
	}
	if rec.Values == nil {
		rec.Values = map[string]any{}
	}
	rec.Values[key] = value
	hosts[name] = rec
	return nil
}

// LookupHost returns the registered Host record for name.
//
// The returned Host is a shallow copy: its Values field is a map, and a
// shallow copy of a struct still aliases the same underlying map as the one
// stored in the registry. Reading rec.Values[key] AFTER this call returns is
// therefore an unsynchronized read racing against SetHostValue, which
// mutates that same aliased map under mu. Callers must not index into the
// returned Host's Values map; use HostValue instead, which performs the
// lookup-and-index atomically under mu.
func LookupHost(name string) (Host, bool) {
	mu.Lock()
	defer mu.Unlock()
	rec, ok := hosts[name]
	return rec, ok
}

// HostValue returns the value stored under key on the registered host name,
// looked up and indexed atomically under a single lock so it is race-free
// against concurrent SetHostValue calls on the same host (unlike calling
// LookupHost and then indexing its Values map after the lock is released).
// hostFound is false when name is not registered at all; keyFound is false
// when the host is registered but key was never set on it.
func HostValue(name, key string) (value any, hostFound, keyFound bool) {
	mu.Lock()
	defer mu.Unlock()
	rec, hostFound := hosts[name]
	if !hostFound {
		return nil, false, false
	}
	value, keyFound = rec.Values[key]
	return value, true, keyFound
}

// AddCluster registers name with the given member host names. hostNames is
// assumed already validated for emptiness/uniqueness by the caller (api's
// checkClusterHostsUnique runs before this, over the caller's own HostRef
// handles); AddCluster re-validates registration invariants that only this
// package's state can answer (every host must already be registered). A
// violation is returned as an error and nothing is registered.
func AddCluster(name string, hostNames []string) (Cluster, error) {
	if name == "" {
		return Cluster{}, fmt.Errorf("Cluster: name must not be empty")
	}
	if len(hostNames) == 0 {
		return Cluster{}, fmt.Errorf("Cluster %q: must include at least one Host", name)
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := clusters[name]; ok {
		return Cluster{}, fmt.Errorf("Cluster %q already registered", name)
	}
	for _, h := range hostNames {
		if _, ok := hosts[h]; !ok {
			return Cluster{}, fmt.Errorf("Cluster %q: Host %q is not registered", name, h)
		}
	}
	rec := Cluster{Name: name, Hosts: append([]string(nil), hostNames...)}
	clusters[name] = rec
	return rec, nil
}

// SetClusterParallel sets cluster name's fan-out width. n < 1 means all
// hosts at once. An unregistered cluster is returned as an error.
func SetClusterParallel(name string, n int) error {
	mu.Lock()
	defer mu.Unlock()
	rec, ok := clusters[name]
	if !ok {
		return fmt.Errorf("Cluster %q is not registered", name)
	}
	if n < 1 {
		rec.Parallelism = -1
	} else {
		rec.Parallelism = n
	}
	clusters[name] = rec
	return nil
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

// AddFleet registers name with the given member cluster names. A violation
// (empty name, no clusters, a duplicate, an unregistered member) is returned
// as an error and nothing is registered.
func AddFleet(name string, clusterNames []string) (Fleet, error) {
	if name == "" {
		return Fleet{}, fmt.Errorf("Fleet: name must not be empty")
	}
	if len(clusterNames) == 0 {
		return Fleet{}, fmt.Errorf("Fleet %q: must include at least one Cluster", name)
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := fleets[name]; ok {
		return Fleet{}, fmt.Errorf("Fleet %q already registered", name)
	}
	for _, c := range clusterNames {
		if _, ok := clusters[c]; !ok {
			return Fleet{}, fmt.Errorf("Fleet %q: Cluster %q is not registered", name, c)
		}
	}
	rec := Fleet{Name: name, Clusters: append([]string(nil), clusterNames...)}
	fleets[name] = rec
	return rec, nil
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
