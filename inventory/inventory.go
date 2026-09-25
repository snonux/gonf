// Package inventory declares the hosts, clusters and fleets gonf pushes to:
// Host with its HostOptions, HostDefaults bundles, typed per-host data
// (WithData), Cluster and Fleet, and the lookups over them.
//
// Package api re-exports every identifier here, so a recipe's single
//
//	import . "github.com/snonux/gonf/api"
//
// reaches them too. This package exists so the inventory half of the DSL has
// its own documentation page; do not dot-import it next to api (Go rejects
// the duplicate names).
package inventory

import (
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
	inv "github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan/seal"
)

// HostRef is an opaque inventory handle for one SSH destination.
// Construct with Host(...); look up later with LookupHost / MustHost.
type HostRef struct {
	name string
}

// HostOption configures a Host at registration.
type HostOption = inv.HostOption

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
	return func(h *inv.Host) error { h.User = user; return nil }
}

// WithSSHHost sets the SSH hostname (default: inventory name, or
// "<name>.<domain>" under WithSSHDomain).
func WithSSHHost(host string) HostOption {
	return func(h *inv.Host) error { h.SSHHost = host; return nil }
}

// WithSSHDomain makes the SSH hostname default to "<inventory name>.<domain>",
// typically in a HostDefaults bundle shared by hosts of one network:
//
//	lan := HostDefaults(WithSSHDomain("lan.buetow.org"))
//	Host("r0", lan) // ssh r0.lan.buetow.org
//
// An explicit WithSSHHost wins, whichever order the options come in.
func WithSSHDomain(domain string) HostOption {
	return func(h *inv.Host) error {
		domain = strings.Trim(domain, ".")
		if domain == "" {
			return fmt.Errorf("WithSSHDomain: domain must not be empty")
		}
		if h.SSHHost == h.Name {
			h.SSHHost = h.Name + "." + domain
		}
		return nil
	}
}

// WithSSHPort sets the SSH port (0 → omit -p).
func WithSSHPort(port int) HostOption {
	return func(h *inv.Host) error { h.Port = port; return nil }
}

// WithSSHIdentity sets ssh -i path.
func WithSSHIdentity(path string) HostOption {
	return func(h *inv.Host) error { h.Identity = path; return nil }
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
	return func(h *inv.Host) error { h.Privilege = mode; return nil }
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
	return func(h *inv.Host) error {
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
	return func(h *inv.Host) error { h.GOOS = goos; return nil }
}

// WithGOARCH sets the GOARCH used when push syncs a newer gonf binary.
// Empty (default) probes via remote uname -m.
func WithGOARCH(goarch string) HostOption {
	return func(h *inv.Host) error { h.GOARCH = goarch; return nil }
}

// WithPlatform sets GOOS and GOARCH from one "goos/goarch" string, the
// form `go tool dist list` prints: WithPlatform("freebsd/amd64") is
// WithGOOS("freebsd") plus WithGOARCH("amd64"). The GOOS must be one gonf
// supports (linux, darwin, freebsd, openbsd, netbsd) and the GOARCH must
// not be empty; anything else is a declaration error.
func WithPlatform(platform string) HostOption {
	return func(h *inv.Host) error {
		goos, goarch, ok := strings.Cut(platform, "/")
		if !ok || goarch == "" || strings.Contains(goarch, "/") {
			return fmt.Errorf("WithPlatform(%q): want \"goos/goarch\", e.g. \"linux/amd64\"", platform)
		}
		switch goos {
		case "linux", "darwin", "freebsd", "openbsd", "netbsd":
		default:
			return fmt.Errorf("WithPlatform(%q): unsupported GOOS %q", platform, goos)
		}
		h.GOOS, h.GOARCH = goos, goarch
		return nil
	}
}

// WithGonfPath sets the remote path for a synced gonf binary (default
// /usr/local/bin/gonf).
func WithGonfPath(path string) HostOption {
	return func(h *inv.Host) error { h.GonfPath = path; return nil }
}

// WithValue stores an arbitrary recipe value under key on this host (e.g. a
// cron window or OnCalendar expression). An empty key or a key already set on
// the same host is registration-time misuse: Host reports it as a declaration
// error (internal/declerr) and does not register the host. The one exception
// is a key set by a HostDefaults bundle, which a later option replaces. Read
// with MustHostValue[T] or ForHosts from task bodies.
func WithValue(key string, value any) HostOption {
	return func(h *inv.Host) error {
		if err := h.PutValue(key, value); err != nil {
			return fmt.Errorf("WithValue: %w", err)
		}
		return nil
	}
}

// SetValue stores an arbitrary recipe value under key on an already-registered
// host (same rules as WithValue). Returns h for chaining. Misuse (an empty
// key, an unregistered host, a key already set) is reported as a declaration
// error (internal/declerr) and stores nothing.
func (h HostRef) SetValue(key string, value any) HostRef {
	declerr.Report(inv.SetHostValue(h.name, key, value))
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
	if _, err := inv.AddHost(name, opts...); err != nil {
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
	_, err := inv.AddCluster(name, names)
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
	declerr.Report(inv.SetClusterParallel(f.name, n))
	return f
}

// LookupHost returns a registered HostRef.
func LookupHost(name string) (HostRef, bool) {
	if _, ok := inv.LookupHost(name); !ok {
		return HostRef{}, false
	}
	return HostRef{name: name}, true
}

// LookupCluster returns a registered ClusterRef.
func LookupCluster(name string) (ClusterRef, bool) {
	if _, ok := inv.LookupCluster(name); !ok {
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
	rec, ok := inv.LookupCluster(f.name)
	if !ok {
		declerr.Reportf("Cluster %q is not registered", f.name)
		return nil
	}
	return append([]string(nil), rec.Hosts...)
}

// Hosts lists registered hosts sorted by name.
func Hosts() []HostInfo {
	recs := inv.HostInfos()
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
	recs := inv.ClusterInfos()
	out := make([]ClusterInfo, 0, len(recs))
	for _, rec := range recs {
		out = append(out, ClusterInfo{
			Name:        rec.Name,
			Hosts:       append([]string(nil), rec.Hosts...),
			Parallelism: inv.ClusterParallelism(rec),
		})
	}
	return out
}

// ResetInventory clears host, cluster, and fleet registries (tests).
func ResetInventory() {
	inv.Reset()
}

// HostDefaults bundles host options into one, so hosts sharing a setup pass
// a single value to Host:
//
//	freebsd := HostDefaults(WithSSHUser("paul"), WithPrivilege(PrivilegeDoas), WithData(Unattended{Hour: "3"}))
//	Host("f0", freebsd, WithSSHHost("f0.lan"))
//
// Options apply in order, bundles expanded in place, so a later option
// overrides an earlier one: a plain field (WithSSHHost, WithPrivilege, ...)
// is simply set again, and a WithValue key or WithData type that a bundle
// set is replaced. A key or type set twice outside any bundle stays a
// declaration error, as before; so a bundle placed after an explicit
// WithValue of the same key is refused rather than silently replacing it.
// Pass bundles first. Bundles nest. Every option runs; the first error
// refuses the host, like any other rejected HostOption.
func HostDefaults(opts ...HostOption) HostOption {
	bundle := slices.Clone(opts)
	return func(h *inv.Host) error { return h.ApplyDefaults(bundle) }
}

// WithData stores v on the host keyed by its concrete type, the typed
// alternative to WithValue: no string key, and the reader names the type.
//
//	type Unattended struct{ Hour, Minute string }
//	Host("f0", WithData(Unattended{Hour: "3", Minute: "10"}))
//	EachHost(func(u Unattended) { /* u is this host's value */ })
//
// Store a struct type of your own rather than a string or int, so two
// recipes never collide on one type. A nil v, or a second value of the same
// type on one host (outside HostDefaults' override rule), is a declaration
// error and the host is not registered.
func WithData(v any) HostOption {
	return func(h *inv.Host) error {
		if err := h.PutData(v); err != nil {
			return fmt.Errorf("WithData: %w", err)
		}
		return nil
	}
}

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
// Registration-time misuse is reported as a declaration error
// (internal/declerr) and the fleet is not registered; the handle is still
// returned.
func Fleet(name string, clusters ...ClusterRef) FleetRef {
	if err := addFleet(name, clusters); err != nil {
		declerr.Report(err)
	}
	return FleetRef{name: name}
}

// addFleet is Fleet's checked core.
func addFleet(name string, clusters []ClusterRef) error {
	if name == "" {
		return fmt.Errorf("Fleet: name must not be empty")
	}
	if len(clusters) == 0 {
		return fmt.Errorf("Fleet %q: must include at least one Cluster", name)
	}
	seen := map[string]struct{}{}
	names := make([]string, len(clusters))
	for i, c := range clusters {
		if c.name == "" {
			return fmt.Errorf("Fleet %q: invalid empty Cluster handle", name)
		}
		if _, ok := seen[c.name]; ok {
			return fmt.Errorf("Fleet %q: duplicate Cluster %q", name, c.name)
		}
		seen[c.name] = struct{}{}
		names[i] = c.name
	}
	_, err := inv.AddFleet(name, names)
	return err
}

// LookupFleet returns a registered FleetRef.
func LookupFleet(name string) (FleetRef, bool) {
	if _, ok := inv.LookupFleet(name); !ok {
		return FleetRef{}, false
	}
	return FleetRef{name: name}, true
}

// MustFleet returns LookupFleet's handle. An unknown name is reported as a
// declaration error (internal/declerr), like MustHost, and the zero FleetRef
// is returned.
func MustFleet(name string) FleetRef {
	f, ok := LookupFleet(name)
	if !ok {
		declerr.Reportf("Fleet %q is not registered", name)
	}
	return f
}

// ClusterNames returns member cluster names in registration order. An unknown
// fleet handle is reported as a declaration error and yields nil.
func (f FleetRef) ClusterNames() []string {
	rec, ok := inv.LookupFleet(f.name)
	if !ok {
		declerr.Reportf("Fleet %q is not registered", f.name)
		return nil
	}
	return append([]string(nil), rec.Clusters...)
}

// HostNames returns unique host inventory names across all member clusters,
// in first-seen registration order. An unknown fleet (or member cluster) is
// reported as a declaration error and yields nil.
func (f FleetRef) HostNames() []string {
	entries, err := inv.CollectFleetHosts(f.name)
	if err != nil {
		declerr.Report(err)
		return nil
	}
	return fleetHostNames(entries)
}

// Fleets lists registered fleets sorted by name.
func Fleets() []FleetInfo {
	recs := inv.FleetInfos()
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

// Name returns the inventory name of this cluster handle.
func (f ClusterRef) Name() string { return f.name }

// Name returns the inventory name of this fleet handle.
func (f FleetRef) Name() string { return f.name }

// fleetHostNames flattens CollectFleetHosts entries to host names.
func fleetHostNames(entries []inv.FleetHostEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.HostName
	}
	return names
}
