package inventory

import (
	"sort"
	"strings"
)

// This file computes the record-time host selection that api.ForHosts
// honours. Narrowing is only an optimisation (skip other hosts' inputs);
// dropping a name whose hostname_contains destination guard matches the
// target would silently drop configuration the old recipe loop applied.
//
// The controller cannot see a push target's live hostname, so push
// selections are approximated from inventory data under one documented
// assumption (also stated in api.ForHosts and docs/design/helpers.md): a machine's
// live hostname contains no inventory name other than those appearing in its
// own inventory name or SSH host, and one machine is registered under one SSH
// host (aliases share the SSHHost, compared case-insensitively like any DNS
// name, and a compatible port). Under that assumption every push selection
// is a superset of the names whose guard
// matches the target; nil means "unknown target: record every host". A
// machine registered under unrelated SSH hosts (e.g. an IP and a DNS name)
// breaks the assumption, and a push to one of its names may skip the other
// name's fragment. Local selections (SelectionForLocalHostname) use the real
// hostname and are exact.

// SelectionForHosts returns the host selection for a push whose targets are
// the registered inventory names given (PushHost, cluster and fleet pushes):
// the targets plus every name that could match one of them at apply time
// (see expandSelectionLocked). Unregistered names are kept as given.
func SelectionForHosts(names []string) []string {
	mu.Lock()
	defer mu.Unlock()
	return expandSelectionLocked(names)
}

// SelectionForDestination returns the host selection for a raw push target
// (`gonf push [-- ssh-args] user@host`, api.PushTo): user and host as in
// remote.PushTarget (host may carry a "user@" prefix), port its Port field,
// and hasExtraSSH whether raw ssh arguments were given.
//
// Only an exact destination narrows. A registered host matches when the
// destination equals its SSHHost or inventory name (case-insensitively, as
// DNS names compare; the CLI's host is taken as typed) and neither an explicit
// user nor an explicit port contradicts the host's own explicit user or port
// (unset on either side means "unspecified"). Contradicting entries are
// skipped, so f3.lan:2201 matches vm1 (f3.lan:2201) but not vm2
// (f3.lan:2202). It returns nil (record every host) when:
//   - any raw ssh argument is present: "-p 2222", "-o HostName=…" or an
//     ssh_config option can redirect the connection to a machine gonf cannot
//     see, so the textual destination proves nothing;
//   - no registered host matches exactly.
//
// Otherwise the matching hosts are expanded exactly like SelectionForHosts.
func SelectionForDestination(user, host string, port int, hasExtraSSH bool) []string {
	if hasExtraSSH {
		return nil
	}
	user, host = splitDestination(user, host)
	if host == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	matched, ok := exactDestinationLocked(user, host, port)
	if !ok {
		return nil
	}
	return expandSelectionLocked(matched)
}

// SelectionForLocalHostname returns the host selection for a local run on a
// machine called hostname: every registered name the hostname contains (case
// insensitive). That is exactly the set whose hostname_contains guard the
// local apply accepts, so the other hosts' ForHosts bodies could never apply
// here. The result is non-nil even when empty: no name matches, so no
// ForHosts body applies locally.
func SelectionForLocalHostname(hostname string) []string {
	lower := strings.ToLower(hostname)
	mu.Lock()
	defer mu.Unlock()
	names := []string{}
	for name := range hosts {
		if strings.Contains(lower, strings.ToLower(name)) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// exactDestinationLocked returns the registered names reachable as
// user@host:port and whether at least one matched (see
// SelectionForDestination). Entries whose explicit user or port contradicts
// the destination's are skipped, not treated as ambiguity. Caller holds mu.
func exactDestinationLocked(user, host string, port int) ([]string, bool) {
	var names []string
	for name, rec := range hosts {
		if !strings.EqualFold(host, rec.SSHHost) && !strings.EqualFold(host, rec.Name) {
			continue
		}
		if user != "" && rec.User != "" && user != rec.User {
			continue
		}
		if !portsCompatible(port, rec.Port) {
			continue
		}
		names = append(names, name)
	}
	return names, len(names) > 0
}

// portsCompatible reports whether two ports can describe the same sshd: equal,
// or at least one unset (0, ssh's own default applies).
func portsCompatible(a, b int) bool {
	return a == 0 || b == 0 || a == b
}

// expandSelectionLocked returns targets plus every registered name N that can
// match a target machine at apply time. The live hostname is not known on the
// controller, so the approximations are generous. It works in two passes:
//
//  1. Machines: each target plus its aliases, i.e. names sharing the
//     target's SSHHost (case-insensitively) with a compatible port (one
//     machine, several names).
//     Different explicit ports on one SSH host (f3.lan:2201 and
//     f3.lan:2202, e.g. VMs behind port forwards) are different machines, so
//     they are not aliases.
//  2. Substrings: every N contained (case insensitive) in ANY name or SSHHost
//     of those machines ("r0" in "r0-wg" or "r0.lan", "pi1" in "pi10"),
//     since the guard is a substring test on the destination's hostname and,
//     by the documented assumption, that hostname may be built from any of
//     the machine's names. Probing aliases too (not only the targets) makes
//     the selection the same whichever of a machine's names is pushed.
//
// No fixpoint is needed: names added in pass 2 belong to other machines, and
// under the documented assumption the target's hostname contains only names
// that appear in its own names or SSH hosts, which pass 2 already probed. The
// aliases of a pass-2 name are therefore not contained in the target's
// hostname and need not be selected.
//
// The result is sorted and never nil. Caller holds mu.
func expandSelectionLocked(targets []string) []string {
	seen := machineNamesLocked(targets)
	var probes []string // lower-cased names and SSH hosts of the machines
	for name := range seen {
		probes = append(probes, strings.ToLower(name))
		if rec, ok := hosts[name]; ok {
			probes = append(probes, strings.ToLower(rec.SSHHost))
		}
	}
	for name := range hosts {
		if containedInAny(name, probes) {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// machineNamesLocked returns the targets plus every registered alias of a
// target (pass 1 of expandSelectionLocked). Unregistered targets are kept as
// given. Aliases are taken relative to the targets only, not transitively: a
// host with an unset port on f3.lan is an alias of both f3.lan:2201 and
// f3.lan:2202, but that does not make those two one machine. Caller holds mu.
func machineNamesLocked(targets []string) map[string]struct{} {
	names := map[string]struct{}{}
	sshPorts := map[string][]int{} // lower-cased target SSHHost -> the targets' ports on it
	for _, t := range targets {
		names[t] = struct{}{}
		if rec, ok := hosts[t]; ok {
			key := strings.ToLower(rec.SSHHost)
			sshPorts[key] = append(sshPorts[key], rec.Port)
		}
	}
	for name, rec := range hosts {
		if isAlias(rec, sshPorts) {
			names[name] = struct{}{}
		}
	}
	return names
}

// isAlias reports whether rec shares an SSH host with a target at a
// compatible port (see portsCompatible).
func isAlias(rec Host, sshPorts map[string][]int) bool {
	for _, port := range sshPorts[strings.ToLower(rec.SSHHost)] {
		if portsCompatible(port, rec.Port) {
			return true
		}
	}
	return false
}

// containedInAny reports whether name (case insensitive) is a substring of
// any of the lower-cased probes.
func containedInAny(name string, probes []string) bool {
	lower := strings.ToLower(name)
	for _, p := range probes {
		if strings.Contains(p, lower) {
			return true
		}
	}
	return false
}

// splitDestination returns the effective ssh user and host of a target. It
// joins user and host the way remote.PushTarget.Destination does and splits
// at the last "@", as ssh itself does, so the CLI form (user empty, host
// "user@host") and the inventory form (separate fields) compare alike.
func splitDestination(user, host string) (string, string) {
	dest := host
	if user != "" {
		dest = user + "@" + host
	}
	i := strings.LastIndex(dest, "@")
	if i < 0 {
		return "", dest
	}
	return dest[:i], dest[i+1:]
}
