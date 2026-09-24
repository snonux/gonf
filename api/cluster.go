package api

import (
	"context"
	"fmt"
	"time"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan/seal"
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
	return func(h *inventory.Host) error { h.User = user; return nil }
}

// WithSSHHost sets the SSH hostname (default: inventory name).
func WithSSHHost(host string) HostOption {
	return func(h *inventory.Host) error { h.SSHHost = host; return nil }
}

// WithSSHPort sets the SSH port (0 → omit -p).
func WithSSHPort(port int) HostOption {
	return func(h *inventory.Host) error { h.Port = port; return nil }
}

// WithSSHIdentity sets ssh -i path.
func WithSSHIdentity(path string) HostOption {
	return func(h *inventory.Host) error { h.Identity = path; return nil }
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
	return func(h *inventory.Host) error { h.Privilege = mode; return nil }
}

// WithPlanRecipient sets this host's age1pq recipient for `gonf plan -seal
// -for host|cluster|fleet` (task 4b2, w82 phase 2, docs/design/plan-encryption.md
// "Keys" and "Operator UX"): a per-host sealed artifact (dir/plan-<host>.age)
// is sealed to this recipient in addition to the operator's own, so the
// destination can decrypt it with the matching identity it holds (typically
// /etc/gonf/identity — see docs/design/plan-encryption.md "Runbook"). The recipient
// is validated with plan/seal.ParseRecipients right here, at registration:
// a malformed value or one that is not an age1pq hybrid recipient (a classic
// X25519, ssh, or plugin recipient) is registration-time misuse, reported as
// a declaration error (internal/declerr) like any other rejected HostOption,
// rather than a silent later refusal when `-for` builds the artifact. A host
// with no WithPlanRecipient is simply not eligible for `-for`: it is refused
// by name before anything is written, never silently skipped.
func WithPlanRecipient(recipient string) HostOption {
	return func(h *inventory.Host) error {
		recipients, err := seal.ParseRecipients([]string{recipient})
		if err != nil {
			return fmt.Errorf("WithPlanRecipient: %w", err)
		}
		if len(recipients) != 1 {
			return fmt.Errorf("WithPlanRecipient: recipient must not be empty or a \"#\" comment")
		}
		// Store the recipient's own canonical age1pq… encoding (Recipient.String),
		// not the raw argument: ParseRecipients re-encodes whatever it parsed,
		// so a copy/paste with incidental leading/trailing whitespace still
		// lands here as the exact bytes plan/seal itself would produce.
		h.PlanRecipient = recipients[0].String()
		return nil
	}
}

// WithGOOS sets the GOOS used when push syncs a newer gonf binary to this host.
// Empty (default) probes via remote uname -s.
func WithGOOS(goos string) HostOption {
	return func(h *inventory.Host) error { h.GOOS = goos; return nil }
}

// WithGOARCH sets the GOARCH used when push syncs a newer gonf binary.
// Empty (default) probes via remote uname -m.
func WithGOARCH(goarch string) HostOption {
	return func(h *inventory.Host) error { h.GOARCH = goarch; return nil }
}

// WithGonfPath sets the remote path for a synced gonf binary (default
// /usr/local/bin/gonf).
func WithGonfPath(path string) HostOption {
	return func(h *inventory.Host) error { h.GonfPath = path; return nil }
}

// WithValue stores an arbitrary recipe value under key on this host (e.g. a
// cron window or OnCalendar expression). An empty key or a key already set on
// the same host is registration-time misuse: Host reports it as a declaration
// error (internal/declerr) and does not register the host. Read with
// MustHostValue[T] from task bodies.
func WithValue(key string, value any) HostOption {
	return func(h *inventory.Host) error {
		if key == "" {
			return fmt.Errorf("WithValue: key must not be empty")
		}
		if _, exists := h.Values[key]; exists {
			return fmt.Errorf("WithValue: key %q already set", key)
		}
		if h.Values == nil {
			h.Values = map[string]any{}
		}
		h.Values[key] = value
		return nil
	}
}

// SetValue stores an arbitrary recipe value under key on an already-registered
// host (same rules as WithValue). Returns h for chaining. Misuse (an empty
// key, an unregistered host, a key already set) is reported as a declaration
// error (internal/declerr) and stores nothing.
func (h HostRef) SetValue(key string, value any) HostRef {
	declerr.Report(inventory.SetHostValue(h.name, key, value))
	return h
}

// Name returns the inventory name of this host handle.
func (h HostRef) Name() string { return h.name }

// Host registers a connection in the host registry and returns a handle.
// Registration-time misuse (empty name, duplicate, a rejected HostOption) is
// reported as a declaration error (internal/declerr, which RecordPlan, Run,
// Apply and the CLI refuse to run with) and the host is not registered; the
// handle is still returned, so later declarations keep being checked.
func Host(name string, opts ...HostOption) HostRef {
	if _, err := inventory.AddHost(name, opts...); err != nil {
		declerr.Report(err)
	}
	return HostRef{name: name}
}

// Cluster registers a named set of HostRef handles. Each host may appear at most
// once. Registration-time misuse is reported as a declaration error
// (internal/declerr) and the cluster is not registered; the handle is still
// returned.
func Cluster(name string, hosts ...HostRef) ClusterRef {
	if err := addCluster(name, hosts); err != nil {
		declerr.Report(err)
	}
	return ClusterRef{name: name}
}

// addCluster is Cluster's checked core.
func addCluster(name string, hosts []HostRef) error {
	if name == "" {
		return fmt.Errorf("Cluster: name must not be empty")
	}
	if len(hosts) == 0 {
		return fmt.Errorf("Cluster %q: must include at least one Host", name)
	}
	if err := checkClusterHostsUnique(hosts); err != nil {
		return fmt.Errorf("Cluster %q: %v", name, err)
	}

	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.name
	}
	_, err := inventory.AddCluster(name, names)
	return err
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

// Parallel sets concurrency for this cluster (default 5). n < 1 means all hosts
// at once. An unregistered cluster is reported as a declaration error
// (internal/declerr).
func (f ClusterRef) Parallel(n int) ClusterRef {
	declerr.Report(inventory.SetClusterParallel(f.name, n))
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

// MustHost returns LookupHost's handle. An unknown name is recipe misuse
// (the Go Must* convention): it is reported as a declaration error
// (internal/declerr, which fails the record or refuses the run) and the zero
// HostRef is returned instead of ending the process.
func MustHost(name string) HostRef {
	h, ok := LookupHost(name)
	if !ok {
		declerr.Reportf("Host %q is not registered", name)
	}
	return h
}

// MustCluster returns LookupCluster's handle. An unknown name is reported as
// a declaration error, like MustHost, and the zero ClusterRef is returned.
func MustCluster(name string) ClusterRef {
	f, ok := LookupCluster(name)
	if !ok {
		declerr.Reportf("Cluster %q is not registered", name)
	}
	return f
}

// HostNames returns the inventory names of hosts in this cluster, in
// registration order. An unknown cluster handle is reported as a declaration
// error (internal/declerr) and yields nil.
func (f ClusterRef) HostNames() []string {
	rec, ok := inventory.LookupCluster(f.name)
	if !ok {
		declerr.Reportf("Cluster %q is not registered", f.name)
		return nil
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
// transport half lives in internal/remote (PushTo → remote.Delivery.ToHost).
// It is PushTo with a ForHosts host selection of this host plus every
// inventory name that could match the same machine (aliases, substrings), so
// other cluster members' ForHosts bodies (and their inputs) are skipped.
func PushHost(h HostRef, tasks ...string) error {
	return runHost(remote.Push, h, "push-"+h.name, tasks)
}

// PreviewHost performs a strict non-mutating remote preview for one host.
// The host must already have a compatible gonf runtime; no binary bootstrap
// occurs. It uses the same ForHosts host selection as PushHost.
func PreviewHost(h HostRef, tasks ...string) error {
	return runHost(remote.Preview, h, "preview-"+h.name, tasks)
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
// shared with PushFleetRun via internal/orchestrate.Deliver.
func PushClusterRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	return groupRun{mode: remote.Push, name: name, planID: planID,
		parallelOverride: parallelOverride, hostTimeout: hostTimeout, tasks: tasks}.cluster(ctx)
}

// PreviewClusterRun records tasks once and performs strict non-mutating
// remote previews across a cluster (planID "" → preview-cluster-<name>).
// Missing or stale remote gonf binaries fail instead of being installed or
// updated.
func PreviewClusterRun(ctx context.Context, name, planID string, parallelOverride int, hostTimeout time.Duration, tasks ...string) error {
	return groupRun{mode: remote.Preview, name: name, planID: planID,
		parallelOverride: parallelOverride, hostTimeout: hostTimeout, tasks: tasks}.cluster(ctx)
}
