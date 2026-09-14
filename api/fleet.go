package api

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

const defaultFleetParallelism = 5

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
}

// FleetRef is an opaque handle for a named set of hosts.
// Construct with Fleet(...HostRef); look up with LookupFleet / MustFleet.
type FleetRef struct {
	name string
}

type fleetRecord struct {
	name        string
	hosts       []HostRef
	parallelism int // 0 → defaultFleetParallelism; <0 → all at once
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

// FleetInfo is a listing row for registered fleets.
type FleetInfo struct {
	Name        string
	Hosts       []string
	Parallelism int
}

var (
	inventoryMu  sync.Mutex
	hostsByName  = map[string]hostRecord{}
	fleetsByName = map[string]fleetRecord{}
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

// Fleet registers a named set of HostRef handles. Each host may appear at most
// once. Registration-time misuse fails fast via logger.Fatal.
func Fleet(name string, hosts ...HostRef) FleetRef {
	if name == "" {
		logger.Fatal("Fleet: name must not be empty")
	}
	if len(hosts) == 0 {
		logger.Fatal("Fleet %q: must include at least one Host", name)
	}
	if err := checkFleetHostsUnique(hosts); err != nil {
		logger.Fatal("Fleet %q: %v", name, err)
	}

	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	if _, ok := fleetsByName[name]; ok {
		logger.Fatal("Fleet %q already registered", name)
	}
	for _, h := range hosts {
		if _, ok := hostsByName[h.name]; !ok {
			logger.Fatal("Fleet %q: Host %q is not registered", name, h.name)
		}
	}
	fleetsByName[name] = fleetRecord{
		name:        name,
		hosts:       append([]HostRef(nil), hosts...),
		parallelism: 0,
	}
	return FleetRef{name: name}
}

func checkFleetHostsUnique(hosts []HostRef) error {
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

// Parallel sets concurrency for this fleet (default 5). n < 1 means all hosts at once.
func (f FleetRef) Parallel(n int) FleetRef {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	rec, ok := fleetsByName[f.name]
	if !ok {
		logger.Fatal("Fleet %q is not registered", f.name)
	}
	if n < 1 {
		rec.parallelism = -1
	} else {
		rec.parallelism = n
	}
	fleetsByName[f.name] = rec
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

// LookupFleet returns a registered FleetRef.
func LookupFleet(name string) (FleetRef, bool) {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	if _, ok := fleetsByName[name]; !ok {
		return FleetRef{}, false
	}
	return FleetRef{name: name}, true
}

// MustHost returns LookupHost or logger.Fatal (Go Must* convention).
func MustHost(name string) HostRef {
	h, ok := LookupHost(name)
	if !ok {
		logger.Fatal("Host %q is not registered", name)
	}
	return h
}

// MustFleet returns LookupFleet or logger.Fatal (Go Must* convention).
func MustFleet(name string) FleetRef {
	f, ok := LookupFleet(name)
	if !ok {
		logger.Fatal("Fleet %q is not registered", name)
	}
	return f
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

// Fleets lists registered fleets sorted by name.
func Fleets() []FleetInfo {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	out := make([]FleetInfo, 0, len(fleetsByName))
	for _, rec := range fleetsByName {
		names := make([]string, len(rec.hosts))
		for i, h := range rec.hosts {
			names[i] = h.name
		}
		p := fleetParallelism(rec)
		out = append(out, FleetInfo{Name: rec.name, Hosts: names, Parallelism: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ResetInventory clears host and fleet registries (tests).
func ResetInventory() {
	inventoryMu.Lock()
	defer inventoryMu.Unlock()
	hostsByName = map[string]hostRecord{}
	fleetsByName = map[string]fleetRecord{}
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
	}, nil
}

func fleetParallelism(rec fleetRecord) int {
	switch {
	case rec.parallelism < 0:
		if n := len(rec.hosts); n > 0 {
			return n
		}
		return 1
	case rec.parallelism == 0:
		return defaultFleetParallelism
	default:
		return rec.parallelism
	}
}

// PushHost records and pushes tasks to one HostRef.
func PushHost(h HostRef, tasks ...string) error {
	t, err := h.pushTarget()
	if err != nil {
		return err
	}
	return PushTo(t, "push-"+h.name, tasks...)
}

// PushFleet records once and fans out the same push payload to every host in the fleet.
func PushFleet(name string, tasks ...string) error {
	return pushFleet(name, "", 0, tasks...)
}

func pushFleet(name, planID string, parallelOverride int, tasks ...string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("fleet %q: no tasks", name)
	}
	inventoryMu.Lock()
	rec, ok := fleetsByName[name]
	if !ok {
		inventoryMu.Unlock()
		return fmt.Errorf("fleet %q is not registered", name)
	}
	hosts := append([]HostRef(nil), rec.hosts...)
	limit := fleetParallelism(rec)
	inventoryMu.Unlock()

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

	var (
		eg     errgroup.Group
		errMu  sync.Mutex
		failed []string
	)
	eg.SetLimit(limit)
	for i := range targets {
		i := i
		eg.Go(func() error {
			if err := pushChunks(targets[i], planID+"-"+labels[i], ops, mem); err != nil {
				errMu.Lock()
				failed = append(failed, fmt.Sprintf("%s: %v", labels[i], err))
				errMu.Unlock()
				return err
			}
			return nil
		})
	}
	_ = eg.Wait()

	okCount := len(targets) - len(failed)
	fmt.Fprintf(os.Stderr, "pushed %s (%d ops) to %s (%d/%d hosts)\n",
		planID, len(ops), name, okCount, len(targets))
	if len(failed) > 0 {
		sort.Strings(failed)
		return fmt.Errorf("fleet %q: %s", name, strings.Join(failed, "; "))
	}
	return nil
}
