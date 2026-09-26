package api

import (
	"errors"
	"fmt"
	"sync"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/plan"
)

// taskClusterStack holds the cluster name associated with the currently running
// task body (RegisterMethods WithCluster). Nested Aggregate → child task pushes
// another frame so ClusterHosts always sees the innermost task's cluster.
var (
	taskClusterMu    sync.Mutex
	taskClusterStack []string
)

func pushTaskCluster(name string) {
	taskClusterMu.Lock()
	defer taskClusterMu.Unlock()
	taskClusterStack = append(taskClusterStack, name)
}

func popTaskCluster() {
	taskClusterMu.Lock()
	defer taskClusterMu.Unlock()
	if len(taskClusterStack) == 0 {
		return
	}
	taskClusterStack = taskClusterStack[:len(taskClusterStack)-1]
}

func currentTaskCluster() string {
	taskClusterMu.Lock()
	defer taskClusterMu.Unlock()
	if len(taskClusterStack) == 0 {
		return ""
	}
	return taskClusterStack[len(taskClusterStack)-1]
}

// ClusterHosts returns List(MustCluster(name).HostNames()...) for the cluster
// associated with the current task via RegisterMethods(..., WithCluster(name)).
// Outside a WithCluster task body it reports a declaration error
// (internal/declerr, which fails the record) and returns nil.
//
// ClusterHosts is a plain inventory listing: it never applies the record-time
// host selection that ForHosts honours, so existing
// WhenHostname(ClusterHosts(), …) recipes keep recording one fragment per
// cluster member regardless of the push target.
func ClusterHosts() []string {
	hosts, err := currentClusterHosts()
	if err != nil {
		declerr.Reportf("ClusterHosts: %w", err)
		return nil
	}
	return hosts
}

// currentClusterHosts is the error-returning core of ClusterHosts, shared with
// ForHosts: the member names of the current task's WithCluster cluster.
func currentClusterHosts() ([]string, error) {
	name := currentTaskCluster()
	if name == "" {
		return nil, fmt.Errorf("no cluster on the current task (RegisterMethods(..., WithCluster(...)))")
	}
	rec, ok := inventory.LookupCluster(name)
	if !ok {
		return nil, fmt.Errorf("Cluster %q is not registered", name)
	}
	return List(rec.Hosts...), nil
}

// ForHosts is the typed current-cluster host iterator. It replaces the
// repeated recipe loop
//
//	for _, host := range ClusterHosts() {
//	    v := MustHostValue[T](host, key)
//	    WhenHostname(host, func() { … })
//	}
//
// with
//
//	ForHosts(key, func(host string, v T) { … })
//
// and keeps exactly those semantics for every host it visits:
//
//   - Hosts come from the current task's WithCluster inventory, in
//     registration order. Inventory stays the only source of per-host values.
//   - Every member's value under key is read and type-checked BEFORE any
//     fragment is recorded, including members outside the host selection
//     below: inventory values are public record-time data, so a missing key or
//     a wrong type is an inventory bug that fails the same way whichever host a
//     run targets.
//   - fn runs inside WhenHostname(host, …): in plan-record mode it is wrapped
//     in a hostname_contains destination guard evaluated on the destination at
//     apply time, never against the controller's hostname. Outside recording
//     it runs only when the local hostname contains host.
//
// Errors: an empty key, a nil fn, a missing WithCluster, or a missing or
// mistyped value records nothing for this call and is reported as a
// declaration error (internal/declerr), like MustSecret. While a plan is being
// recorded (Run, gonf plan, push, cluster, fleet) it fails the record:
// RecordPlan/Run/push return it, Run's temporary plan directory is removed,
// and no SSH connection is opened. Outside recording (a direct call from Go
// code) api.Apply and the CLI refuse with it, like MustHostValue.
//
// Host selection: every recording entry point that knows where the plan will
// apply records with a host selection (see recordPlanForHosts), and ForHosts
// skips fn for cluster members outside it, so their inputs — e.g. a per-host
// MustSecret read inside fn — are never resolved. A push selection contains
// the target's names plus every alias sharing its SSH host and every name
// contained in its inventory name or SSH host (the guard is a substring test
// on the live hostname, which only the destination knows); a target that
// cannot be identified exactly (raw ssh arguments, unknown destination,
// contradicting user or port) records every member. A local Run selects the
// names its own hostname contains, which is exact. Read host-specific inputs
// inside fn, not before calling ForHosts.
func ForHosts[T any](key string, fn func(host string, value T)) {
	var argErr error
	if key == "" {
		argErr = fmt.Errorf("key must not be empty")
	}
	visitClusterHosts("ForHosts", argErr, fn, func(host string) (T, error) {
		return lookupHostValue[T](host, key)
	})
}

// visitClusterHosts is the shared core of ForHosts and EachHost: it resolves
// every current cluster member's value with lookup, then runs fn inside
// WhenHostname(host, ...) for each selected member (see ForHosts for the
// full contract). argErr is the caller's own argument check, reported
// before the nil-fn check. Any failure records nothing and is reported as a
// declaration error prefixed with caller.
func visitClusterHosts[T any](caller string, argErr error, fn func(host string, v T), lookup func(host string) (T, error)) {
	hosts, values, skipped, err := clusterHostValues(argErr, fn != nil, lookup)
	if err != nil {
		// Captured into the current recording session (the record then
		// fails with it, see stashBodyError) or, outside recording, kept
		// for Apply and the CLI.
		declerr.Report(fmt.Errorf("%s: %w", caller, err))
		return
	}
	for i, host := range hosts {
		if !hostSelected(host) || skipped[i] {
			continue
		}
		whenHostnameOne(hostnameMatch(host), func() { fn(host, values[i]) })
	}
}

// errSkipHost is returned by a visitClusterHosts lookup for a member the
// visit leaves out without an error (EachHostWith's member without a value).
var errSkipHost = errors.New("skip host")

// clusterHostValues validates the arguments and resolves every current
// cluster member's value, in member order. Resolving all values first means
// a bad entry for a later host can never leave a partially recorded set of
// fragments behind it.
func clusterHostValues[T any](argErr error, haveFn bool, lookup func(host string) (T, error)) ([]string, []T, []bool, error) {
	if argErr != nil {
		return nil, nil, nil, argErr
	}
	if !haveFn {
		return nil, nil, nil, fmt.Errorf("fn must not be nil")
	}
	hosts, err := currentClusterHosts()
	if err != nil {
		return nil, nil, nil, err
	}
	values := make([]T, len(hosts))
	skipped := make([]bool, len(hosts))
	for i, host := range hosts {
		values[i], err = lookup(host)
		switch {
		case errors.Is(err, errSkipHost):
			skipped[i] = true
		case err != nil:
			return nil, nil, nil, err
		}
	}
	return hosts, values, skipped, nil
}

// hostSelection is the record-time set of inventory host names the current
// recording is destined for. nil means "no selection": every host counts as
// selected (gonf plan, RecordPlan, and pushes whose target cannot be
// identified exactly).
//
// It is process-global state scoped to one recording session, and plan
// recording is single-goroutine by the DSL invariant (recSession, api/plan.go):
// two recordings must never run concurrently, whether they are pushes, local
// runs or `gonf plan`, because they would also share recSession and the plan
// recorder. No other goroutine touches the selection: recordPlanForHosts
// restores it before any push fans out, and the fan-out never records. The
// mutex is purely defensive; it does not make concurrent recordings with
// different selections safe.
var (
	hostSelectionMu sync.Mutex
	hostSelection   map[string]struct{}
)

// hostSelected reports whether ForHosts should visit name in the current
// recording.
func hostSelected(name string) bool {
	hostSelectionMu.Lock()
	defer hostSelectionMu.Unlock()
	if hostSelection == nil {
		return true
	}
	_, ok := hostSelection[name]
	return ok
}

// setHostSelection installs hosts as the selection and returns a function
// restoring the previous one. A nil hosts slice clears the selection (all
// hosts selected); an empty non-nil slice selects none.
func setHostSelection(hosts []string) (restore func()) {
	hostSelectionMu.Lock()
	defer hostSelectionMu.Unlock()
	prev := hostSelection
	if hosts == nil {
		hostSelection = nil
	} else {
		hostSelection = make(map[string]struct{}, len(hosts))
		for _, h := range hosts {
			hostSelection[h] = struct{}{}
		}
	}
	return func() {
		hostSelectionMu.Lock()
		defer hostSelectionMu.Unlock()
		hostSelection = prev
	}
}

// recordPlanForHosts is RecordPlanTo with the record-time host selection set
// to hosts for the duration of the recording (see ForHosts); nil hosts
// records exactly like RecordPlanTo. The deferred restore runs on every
// return path, including an error or a panic in a task body.
func recordPlanForHosts(hosts []string, planID string, store plan.BlobStore, tasks ...string) ([]plan.Op, error) {
	defer setHostSelection(hosts)()
	return RecordPlanTo(planID, store, tasks...)
}

// RecordPlanForHost records tasks with the same ForHosts host selection a
// push to host would use (inventory.SelectionForHosts: host plus every
// inventory name that could match the same machine at apply time, exactly
// like PushHost/runHost). It is `gonf plan -seal -for`'s (task 4b2, w82
// phase 2) recording entry point: called once per target host, so a
// ForHosts body for a host outside that selection is never resolved and its
// inputs (e.g. a per-host MustSecret read inside it) are never read into
// THIS host's artifact — see docs/design/plan-encryption.md "Operator UX", the
// `-for` row's "records once per host" rule, which exists specifically so a
// per-host sealed artifact does not carry an unrelated host's secret
// material. This is NOT a guarantee that only THIS host's own ForHosts body
// is ever resolved: SelectionForHosts is a substring-based superset, the same
// one PushHost/runHost themselves rely on (internal/inventory/destination.go),
// so a host whose name or SSHHost is a substring of host's (or vice versa)
// is IN the selection too, and its ForHosts body — and any secret it reads —
// physically lands in host's sealed artifact, typically wrapped in a
// when_begin/hostname_contains guard that will not match host's real live
// hostname at apply time (task ng2; see docs/design/plan-encryption.md's "Runbook"
// for the operator-facing caveat and
// TestCLIPlanSealForNameSubstringCarriesOtherHostsSecret /
// TestCLIPlanSealForSSHHostSubstringCarriesUnrelatedHostsSecret for the
// pinned regression cases).
func RecordPlanForHost(host, planID string, store plan.BlobStore, tasks ...string) ([]plan.Op, error) {
	return recordPlanForHosts(inventory.SelectionForHosts([]string{host}), planID, store, tasks...)
}

// PlanRecipientTargetHosts resolves name — `gonf plan -seal -for`'s argument
// (task 4b2) — to the host names to seal a per-host plan for: a registered
// host by itself, or every member of a registered cluster or fleet (a
// fleet's members are already deduplicated across its clusters, see
// FleetRef.HostNames). A host name takes priority over a same-named cluster
// or fleet, then a cluster over a fleet, matching how the three registries
// are otherwise looked up independently (Host/Cluster/Fleet share no
// namespace, so a genuine collision is rare, but -for must still pick one
// deterministically rather than erroring on an ambiguity nothing else in
// gonf treats as one). A name that matches none of the three is refused by
// name, never silently treated as an empty selection.
func PlanRecipientTargetHosts(name string) ([]string, error) {
	if h, ok := LookupHost(name); ok {
		return []string{h.Name()}, nil
	}
	if c, ok := LookupCluster(name); ok {
		return c.HostNames(), nil
	}
	if f, ok := LookupFleet(name); ok {
		return f.HostNames(), nil
	}
	return nil, fmt.Errorf("-for: %q is not a registered host, cluster or fleet", name)
}

// HostPlanRecipient returns the age1pq recipient api.WithPlanRecipient set
// on the registered host name, and whether one was set at all (false for an
// unregistered host too). Used by `gonf plan -seal -for` (task 4b2) to check
// every target host has a recipient before sealing anything, and to build
// each host's own recipient list.
func HostPlanRecipient(name string) (string, bool) {
	rec, ok := inventory.LookupHost(name)
	if !ok || rec.PlanRecipient == "" {
		return "", false
	}
	return rec.PlanRecipient, true
}

// localHostSelection is the selection of a local Run: the inventory names the
// local hostname contains — exactly the names whose destination guard the
// local apply accepts, so no other host's ForHosts body could apply here.
func localHostSelection() []string {
	return inventory.SelectionForLocalHostname(DetectFacts().Hostname)
}

// resetTaskCluster clears the cluster stack and the host selection (tests).
func resetTaskCluster() {
	taskClusterMu.Lock()
	taskClusterStack = nil
	taskClusterMu.Unlock()
	setHostSelection(nil)
}
