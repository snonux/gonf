package api

import (
	"context"
	"fmt"
	"time"

	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/orchestrate"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// HostRef is an opaque inventory handle for one SSH destination.
// Construct with Host(...); look up later with LookupHost / MustHost.
type HostRef struct {
	name string
}

// HostOption configures a Host at registration.
type HostOption = inventory.HostOption

// ClusterRef is an opaque handle for a named set of hosts.
// Construct with Cluster(...HostRef); look up with LookupCluster / MustCluster.
type ClusterRef struct {
	name string
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

// WithSSHUser sets the SSH username (empty → ssh default).
// Named WithSSHUser so it does not clash with options.WithUser (systemd).
func WithSSHUser(user string) HostOption {
	return func(h *inventory.Host) { h.User = user }
}

// WithSSHHost sets the SSH hostname (default: inventory name).
func WithSSHHost(host string) HostOption {
	return func(h *inventory.Host) { h.SSHHost = host }
}

// WithSSHPort sets the SSH port (0 → omit -p).
func WithSSHPort(port int) HostOption {
	return func(h *inventory.Host) { h.Port = port }
}

// WithSSHIdentity sets ssh -i path.
func WithSSHIdentity(path string) HostOption {
	return func(h *inventory.Host) { h.Identity = path }
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
	return func(h *inventory.Host) { h.Privilege = mode }
}

// WithGOOS sets the GOOS used when push syncs a newer gonf binary to this host.
// Empty (default) probes via remote uname -s.
func WithGOOS(goos string) HostOption {
	return func(h *inventory.Host) { h.GOOS = goos }
}

// WithGOARCH sets the GOARCH used when push syncs a newer gonf binary.
// Empty (default) probes via remote uname -m.
func WithGOARCH(goarch string) HostOption {
	return func(h *inventory.Host) { h.GOARCH = goarch }
}

// WithGonfPath sets the remote path for a synced gonf binary (default
// /usr/local/bin/gonf).
func WithGonfPath(path string) HostOption {
	return func(h *inventory.Host) { h.GonfPath = path }
}

// WithValue stores an arbitrary recipe value under key on this host (e.g. a
// cron window or OnCalendar expression). Duplicate keys on the same host fail
// fast. Read with MustHostValue[T] from task bodies.
func WithValue(key string, value any) HostOption {
	return func(h *inventory.Host) {
		if key == "" {
			logger.Fatal("WithValue: key must not be empty")
		}
		if h.Values == nil {
			h.Values = map[string]any{}
		}
		if _, exists := h.Values[key]; exists {
			logger.Fatal("WithValue: key %q already set", key)
		}
		h.Values[key] = value
	}
}

// SetValue stores an arbitrary recipe value under key on an already-registered
// host (same rules as WithValue). Returns h for chaining.
func (h HostRef) SetValue(key string, value any) HostRef {
	if key == "" {
		logger.Fatal("SetValue: key must not be empty")
	}
	inventory.SetHostValue(h.name, key, value)
	return h
}

// Name returns the inventory name of this host handle.
func (h HostRef) Name() string { return h.name }

// Host registers a connection in the host registry and returns a handle.
// Registration-time misuse (empty name, duplicate) fails fast via
// logger.Fatal.
func Host(name string, opts ...HostOption) HostRef {
	inventory.AddHost(name, opts...)
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

	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.name
	}
	inventory.AddCluster(name, names)
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
	inventory.SetClusterParallel(f.name, n)
	return f
}

// LookupHost returns a registered HostRef.
func LookupHost(name string) (HostRef, bool) {
	if _, ok := inventory.LookupHost(name); !ok {
		return HostRef{}, false
	}
	return HostRef{name: name}, true
}

// LookupCluster returns a registered ClusterRef.
func LookupCluster(name string) (ClusterRef, bool) {
	if _, ok := inventory.LookupCluster(name); !ok {
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
	rec, ok := inventory.LookupCluster(f.name)
	if !ok {
		logger.Fatal("Cluster %q is not registered", f.name)
	}
	return append([]string(nil), rec.Hosts...)
}

// Hosts lists registered hosts sorted by name.
func Hosts() []HostInfo {
	recs := inventory.HostInfos()
	out := make([]HostInfo, 0, len(recs))
	for _, rec := range recs {
		out = append(out, HostInfo{
			Name:      rec.Name,
			User:      rec.User,
			SSHHost:   rec.SSHHost,
			Port:      rec.Port,
			Identity:  rec.Identity,
			Privilege: rec.Privilege.String(),
		})
	}
	return out
}

// Clusters lists registered clusters sorted by name.
func Clusters() []ClusterInfo {
	recs := inventory.ClusterInfos()
	out := make([]ClusterInfo, 0, len(recs))
	for _, rec := range recs {
		out = append(out, ClusterInfo{
			Name:        rec.Name,
			Hosts:       append([]string(nil), rec.Hosts...),
			Parallelism: inventory.ClusterParallelism(rec),
		})
	}
	return out
}

// ResetInventory clears host, cluster, and fleet registries (tests).
func ResetInventory() {
	inventory.Reset()
}

func (h HostRef) pushTarget() (PushTarget, error) {
	return inventory.PushTargetFor(h.name)
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

// PreviewHost performs a strict non-mutating remote preview for one host.
// The host must already have a compatible gonf runtime; no binary bootstrap
// occurs.
func PreviewHost(h HostRef, tasks ...string) error {
	t, err := h.pushTarget()
	if err != nil {
		return err
	}
	return PreviewTo(t, "preview-"+h.name, tasks...)
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
// shared with PushFleetRun via internal/orchestrate.Push.
func PushClusterRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	return runCluster(ctx, name, planID, parallelOverride, hostTimeout, false, tasks...)
}

// PreviewClusterRun records tasks once and performs strict non-mutating
// remote previews across a cluster. Missing or stale remote gonf binaries
// fail instead of being installed or updated.
func PreviewClusterRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	return runCluster(ctx, name, planID, parallelOverride, hostTimeout, true, tasks...)
}

func runCluster(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, strictPreview bool, tasks ...string) error {
	if len(tasks) == 0 {
		if strictPreview {
			return fmt.Errorf("cluster preview %q: no tasks", name)
		}
		return fmt.Errorf("cluster %q: no tasks", name)
	}
	rec, ok := inventory.LookupCluster(name)
	if !ok {
		return fmt.Errorf("cluster %q is not registered", name)
	}
	limit := inventory.ClusterParallelism(rec)

	if parallelOverride > 0 {
		limit = parallelOverride
	}
	if planID == "" {
		if strictPreview {
			planID = "preview-cluster-" + name
		} else {
			planID = "cluster-" + name
		}
	}

	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	label := fmt.Sprintf("cluster %q", name)
	if strictPreview {
		label = fmt.Sprintf("cluster preview %q", name)
	}
	if err := RefuseOpaqueOnlyPush(label); err != nil {
		return err
	}
	if strictPreview {
		return orchestrate.Preview(ctx, name, planID, rec.Hosts, limit, hostTimeout, ops, mem)
	}
	return orchestrate.Push(ctx, name, planID, rec.Hosts, limit, hostTimeout, ops, mem)
}
