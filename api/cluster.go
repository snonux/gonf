package api

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

const defaultClusterParallelism = 5

// HostRef is an opaque inventory handle for one SSH destination.
// Construct with Host(...); look up later with LookupHost / MustHost.
type HostRef struct {
	name string
}

// HostOption configures a Host at registration.
type HostOption func(*hostRecord)

type hostRecord struct {
	name      string
	user      string
	sshHost   string
	port      int
	identity  string
	privilege privilege.Mode
	values    map[string]any // arbitrary per-host recipe values (WithValue / SetValue)
	goos      string
	goarch    string
	gonfPath  string
}

// ClusterRef is an opaque handle for a named set of hosts.
// Construct with Cluster(...HostRef); look up with LookupCluster / MustCluster.
type ClusterRef struct {
	name string
}

type clusterRecord struct {
	name        string
	hosts       []HostRef
	parallelism int // 0 → defaultClusterParallelism; <0 → all at once
}

// HostInfo is a listing row for registered hosts.
type HostInfo struct {
	Name      string
	User      string
	SSHHost   string
	Port      int
	Identity  string
	Privilege string
}

// ClusterInfo is a listing row for registered clusters.
type ClusterInfo struct {
	Name        string
	Hosts       []string
	Parallelism int
}

var (
	inventoryMu  sync.Mutex
	hostsByName  = map[string]hostRecord{}
	clustersByName = map[string]clusterRecord{}
)

// WithSSHUser sets the SSH username (empty → ssh default).
// Named WithSSHUser so it does not clash with options.WithUser (systemd).
func WithSSHUser(user string) HostOption {
	return func(h *hostRecord) { h.user = user }
}

// WithSSHHost sets the SSH hostname (default: inventory name).
func WithSSHHost(host string) HostOption {
	return func(h *hostRecord) { h.sshHost = host }
}

// WithSSHPort sets the SSH port (0 → omit -p).
func WithSSHPort(port int) HostOption {
	return func(h *hostRecord) { h.port = port }
}

// WithSSHIdentity sets ssh -i path.
func WithSSHIdentity(path string) HostOption {
	return func(h *hostRecord) { h.identity = path }
}

// PrivilegeNone, PrivilegeSudo, and PrivilegeDoas re-export the
// privilege.Mode constants so tasks can pass them to WithPrivilege without
// importing an internal package.
const (
	PrivilegeNone = privilege.None
	PrivilegeSudo = privilege.Sudo
	PrivilegeDoas = privilege.Doas
)

// WithPrivilege sets how privileged apply chunks are wrapped on this host.
func WithPrivilege(mode privilege.Mode) HostOption {
	return func(h *hostRecord) { h.privilege = mode }
}

// WithGOOS sets the GOOS used when push syncs a newer gonf binary to this host.
// Empty (default) probes via remote uname -s.
func WithGOOS(goos string) HostOption {
	return func(h *hostRecord) { h.goos = goos }
}

// WithGOARCH sets the GOARCH used when push syncs a newer gonf binary.
// Empty (default) probes via remote uname -m.
func WithGOARCH(goarch string) HostOption {
	return func(h *hostRecord) { h.goarch = goarch }
}

// WithGonfPath sets the remote path for a synced gonf binary (default
// /usr/local/bin/gonf).
func WithGonfPath(path string) HostOption {
	return func(h *hostRecord) { h.gonfPath = path }
}

// WithValue stores an arbitrary recipe value under key on this host (e.g. a
// cron window or OnCalendar expression). Duplicate keys on the same host fail
// fast. Read with MustHostValue[T] from task bodies.
func WithValue(key string, value any) HostOption {
	return func(h *hostRecord) {
		if key == "" {
			logger.Fatal("WithValue: key must not be empty")
		}
		if h.values == nil {
			h.values = map[string]any{}
		}
		if _, exists := h.values[key]; exists {
			logger.Fatal("WithValue: key %q already set", key)
		}
		h.values[key] = value
	}
}

// SetValue stores an arbitrary recipe value under key on an already-registered
// host (same rules as WithValue). Returns h for chaining.
func (h HostRef) SetValue(key string, value any) HostRef {
	if key == "" {
		logger.Fatal("SetValue: key must not be empty")
	}
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := hostsByName[h.name]
	if !ok {
		logger.Fatal("SetValue: Host %q is not registered", h.name)
	}
	if rec.values == nil {
		rec.values = map[string]any{}
	}
	if _, exists := rec.values[key]; exists {
		logger.Fatal("Host %q: value key %q already set", h.name, key)
	}
	rec.values[key] = value
	hostsByName[h.name] = rec
	return h
}

// Name returns the inventory name of this host handle.
func (h HostRef) Name() string { return h.name }

// Host registers a connection in the host registry and returns a handle.
// Registration-time misuse (empty name, duplicate) fails fast via
// logger.Fatal.
func Host(name string, opts ...HostOption) HostRef {
	if name == "" {
		logger.Fatal("Host: name must not be empty")
	}
	rec := hostRecord{name: name, sshHost: name}
	for _, o := range opts {
		o(&rec)
	}
	if rec.sshHost == "" {
		rec.sshHost = name
	}

	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	if _, ok := hostsByName[name]; ok {
		logger.Fatal("Host %q already registered", name)
	}
	hostsByName[name] = rec
	return HostRef{name: name}
}

// Cluster registers a named set of HostRef handles. Each host may appear at most
// once. Registration-time misuse fails fast via logger.Fatal.
func Cluster(name string, hosts ...HostRef) ClusterRef {
	if name == "" {
		logger.Fatal("Cluster: name must not be empty")
	}
	if len(hosts) == 0 {
		logger.Fatal("Cluster %q: must include at least one Host", name)
	}
	if err := checkClusterHostsUnique(hosts); err != nil {
		logger.Fatal("Cluster %q: %v", name, err)
	}

	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	if _, ok := clustersByName[name]; ok {
		logger.Fatal("Cluster %q already registered", name)
	}
	for _, h := range hosts {
		if _, ok := hostsByName[h.name]; !ok {
			logger.Fatal("Cluster %q: Host %q is not registered", name, h.name)
		}
	}
	clustersByName[name] = clusterRecord{
		name:        name,
		hosts:       append([]HostRef(nil), hosts...),
		parallelism: 0,
	}
	return ClusterRef{name: name}
}

func checkClusterHostsUnique(hosts []HostRef) error {
	seen := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if h.name == "" {
			return fmt.Errorf("invalid empty Host handle")
		}
		if _, ok := seen[h.name]; ok {
			return fmt.Errorf("duplicate Host %q", h.name)
		}
		seen[h.name] = struct{}{}
	}
	return nil
}

// Parallel sets concurrency for this cluster (default 5). n < 1 means all hosts at once.
func (f ClusterRef) Parallel(n int) ClusterRef {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := clustersByName[f.name]
	if !ok {
		logger.Fatal("Cluster %q is not registered", f.name)
	}
	if n < 1 {
		rec.parallelism = -1
	} else {
		rec.parallelism = n
	}
	clustersByName[f.name] = rec
	return f
}

// LookupHost returns a registered HostRef.
func LookupHost(name string) (HostRef, bool) {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	if _, ok := hostsByName[name]; !ok {
		return HostRef{}, false
	}
	return HostRef{name: name}, true
}

// LookupCluster returns a registered ClusterRef.
func LookupCluster(name string) (ClusterRef, bool) {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	if _, ok := clustersByName[name]; !ok {
		return ClusterRef{}, false
	}
	return ClusterRef{name: name}, true
}

// MustHost returns LookupHost or logger.Fatal (Go Must* convention).
func MustHost(name string) HostRef {
	h, ok := LookupHost(name)
	if !ok {
		logger.Fatal("Host %q is not registered", name)
	}
	return h
}

// MustCluster returns LookupCluster or logger.Fatal (Go Must* convention).
func MustCluster(name string) ClusterRef {
	f, ok := LookupCluster(name)
	if !ok {
		logger.Fatal("Cluster %q is not registered", name)
	}
	return f
}

// HostNames returns the inventory names of hosts in this cluster, in
// registration order. An unknown cluster handle fails fast via logger.Fatal.
func (f ClusterRef) HostNames() []string {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := clustersByName[f.name]
	if !ok {
		logger.Fatal("Cluster %q is not registered", f.name)
	}
	names := make([]string, len(rec.hosts))
	for i, h := range rec.hosts {
		names[i] = h.name
	}
	return names
}

// Hosts lists registered hosts sorted by name.
func Hosts() []HostInfo {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	out := make([]HostInfo, 0, len(hostsByName))
	for _, rec := range hostsByName {
		out = append(out, HostInfo{
			Name:      rec.name,
			User:      rec.user,
			SSHHost:   rec.sshHost,
			Port:      rec.port,
			Identity:  rec.identity,
			Privilege: rec.privilege.String(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Clusters lists registered clusters sorted by name.
func Clusters() []ClusterInfo {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	out := make([]ClusterInfo, 0, len(clustersByName))
	for _, rec := range clustersByName {
		names := make([]string, len(rec.hosts))
		for i, h := range rec.hosts {
			names[i] = h.name
		}
		p := clusterParallelism(rec)
		out = append(out, ClusterInfo{Name: rec.name, Hosts: names, Parallelism: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ResetInventory clears host, cluster, and fleet registries (tests).
func ResetInventory() {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	hostsByName = map[string]hostRecord{}
	clustersByName = map[string]clusterRecord{}
	fleetsByName = map[string]fleetOfClustersRecord{}
}

func (h HostRef) pushTarget() (PushTarget, error) {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := hostsByName[h.name]
	if !ok {
		return PushTarget{}, fmt.Errorf("host %q is not registered", h.name)
	}
	return PushTarget{
		User:      rec.user,
		Host:      rec.sshHost,
		Port:      rec.port,
		Identity:  rec.identity,
		Privilege: rec.privilege,
		GOOS:      rec.goos,
		GOARCH:    rec.goarch,
		GonfPath:  rec.gonfPath,
	}, nil
}

func clusterParallelism(rec clusterRecord) int {
	switch {
	case rec.parallelism < 0:
		if n := len(rec.hosts); n > 0 {
			return n
		}
		return 1
	case rec.parallelism == 0:
		return defaultClusterParallelism
	default:
		return rec.parallelism
	}
}

// PushHost records and pushes tasks to one HostRef. Thin wrapper: the
// transport half lives in internal/remote (PushTo → remote.PushChunks).
func PushHost(h HostRef, tasks ...string) error {
	t, err := h.pushTarget()
	if err != nil {
		return err
	}
	return PushTo(t, "push-"+h.name, tasks...)
}

// PushCluster records once and fans out the same push payload to every host in
// the cluster. Thin wrapper: it runs the full-parameter PushClusterRun with the
// library defaults (no context, no per-run overrides).
func PushCluster(name string, tasks ...string) error {
	return PushClusterRun(context.Background(), name, "", 0, remote.DefaultHostTimeout, tasks...)
}

// PushClusterRun records tasks once and fans the same push out to every host in
// the named cluster. It is the full-parameter form of PushCluster: the CLI
// (gonf cluster) uses it to thread its signal-derived context and per-run
// overrides through — planID ("" → cluster-<name>), parallelOverride (> 0
// overrides the cluster's parallelism), and hostTimeout (per-host push bound;
// <= 0 means unlimited). The per-host transport fan-out itself lives in
// internal/remote (remote.Fanout); the record-once-then-fan-out plumbing is
// shared with PushFleetRun via pushHosts.
func PushClusterRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("cluster %q: no tasks", name)
	}
	inventoryMu.Lock()
	rec, ok := clustersByName[name]
	if !ok {
		inventoryMu.Unlock()
		return fmt.Errorf("cluster %q is not registered", name)
	}
	hosts := append([]HostRef(nil), rec.hosts...)
	limit := clusterParallelism(rec)
	inventoryMu.Unlock()

	if parallelOverride > 0 {
		limit = parallelOverride
	}
	if planID == "" {
		planID = "cluster-" + name
	}

	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := RefuseOpaqueOnlyPush(fmt.Sprintf("cluster %q", name)); err != nil {
		return err
	}
	return pushHosts(ctx, name, planID, hosts, limit, hostTimeout, ops, mem)
}

// pushHosts fans an already-recorded plan (ops/mem, produced by exactly one
// RecordPlanTo call — recording uses package-level global state in the plan
// and resource packages and is not safe to run concurrently or repeatedly
// for one push) out to hosts via remote.Fanout, bounded by limit concurrent
// per-host pushes. name labels the Fanout summary line and error messages
// (a cluster name for both PushClusterRun and each of PushFleetRun's
// per-cluster groups).
//
// This is the single push pipeline shared by PushClusterRun (one call, the
// whole cluster) and PushFleetRun (one call per member cluster's host
// group — see PushFleetRun's doc comment for why parallelism is applied per
// group instead of once for the whole fleet). Before this helper existed,
// PushClusterRun and PushFleetRun each built targets/labels and called
// remote.Fanout with their own near-identical copy of this loop.
func pushHosts(ctx context.Context, name, planID string, hosts []HostRef, limit int, hostTimeout time.Duration, ops []plan.Op, mem plan.BlobReader) error {
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
	return remote.Fanout(ctx, name, planID, ops, mem, targets, labels, limit, hostTimeout)
}
