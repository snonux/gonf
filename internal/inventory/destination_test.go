package inventory

import (
	"reflect"
	"testing"
)

// registerSelectionInventory registers the review's alias inventory:
// r0 (root@r0.lan) and its alias r0-wg (r0.lan), r1 (r1.lan), pi1 and pi10
// (inventory name as SSH host), and fishfinger (rex@fishfinger.example:2).
func registerSelectionInventory(t *testing.T) {
	t.Helper()
	Reset()
	t.Cleanup(Reset)
	mustAddHost(t, "r0", func(h *Host) { h.User, h.SSHHost = "root", "r0.lan" })
	mustAddHost(t, "r0-wg", func(h *Host) { h.SSHHost = "r0.lan" })
	mustAddHost(t, "r1", func(h *Host) { h.SSHHost = "r1.lan" })
	mustAddHost(t, "pi1")
	mustAddHost(t, "pi10")
	mustAddHost(t, "fishfinger", func(h *Host) {
		h.User, h.SSHHost, h.Port = "rex", "fishfinger.example", 2
	})
	// Two VMs behind port forwards on one SSH host: different machines.
	mustAddHost(t, "vm1", func(h *Host) { h.SSHHost, h.Port = "f3.lan", 2201 })
	mustAddHost(t, "vm2", func(h *Host) { h.SSHHost, h.Port = "f3.lan", 2202 })
}

// TestSelectionForHosts pins that a push to known inventory names never
// selects fewer names than could match the target machine: aliases sharing
// its SSH host and names contained in its name or SSH host are added.
func TestSelectionForHosts(t *testing.T) {
	registerSelectionInventory(t)
	cases := []struct {
		name    string
		targets []string
		want    []string
	}{
		{"alias target adds the name sharing its ssh host", []string{"r0-wg"}, []string{"r0", "r0-wg"}},
		{"primary target adds its alias", []string{"r0"}, []string{"r0", "r0-wg"}},
		{"substring name is added", []string{"pi10"}, []string{"pi1", "pi10"}},
		{"longer name is not added", []string{"pi1"}, []string{"pi1"}},
		{"unrelated host stays alone", []string{"r1"}, []string{"r1"}},
		{"different ports on one ssh host are not aliases", []string{"vm1"}, []string{"vm1"}},
		{"cluster with an alias member", []string{"r0-wg", "r1"}, []string{"r0", "r0-wg", "r1"}},
		{"unregistered name is kept", []string{"ghost"}, []string{"ghost"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectionForHosts(tc.targets); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SelectionForHosts(%v) = %v, want %v", tc.targets, got, tc.want)
			}
		})
	}
}

// TestSelectionForDestination pins which raw push destinations narrow. Every
// destination that is not an exact, unambiguous inventory match must return
// nil (record every host) rather than a selection that could miss the
// machine's own fragment.
func TestSelectionForDestination(t *testing.T) {
	registerSelectionInventory(t)
	cases := []struct {
		name       string
		user, host string
		port       int
		extraSSH   bool
		want       []string
	}{
		{"cli user@host form", "", "rex@fishfinger.example", 0, false, []string{"fishfinger"}},
		{"separate user and port fields", "rex", "fishfinger.example", 2, false, []string{"fishfinger"}},
		{"bare shared ssh host selects every alias", "", "r0.lan", 0, false, []string{"r0", "r0-wg"}},
		{"matching explicit user keeps aliases", "", "root@r0.lan", 0, false, []string{"r0", "r0-wg"}},
		{"inventory name as ssh alias", "", "r0-wg", 0, false, []string{"r0", "r0-wg"}},
		{"substring names are added", "", "pi10", 0, false, []string{"pi1", "pi10"}},
		// Not exact: every host is recorded.
		// A contradicting entry is skipped when another entry matches
		// exactly; the match is then expanded to its aliases again.
		{"explicit user skips the contradicting alias", "", "paul@r0.lan", 0, false, []string{"r0", "r0-wg"}},
		{"explicit port selects its own vm", "", "f3.lan", 2201, false, []string{"vm1"}},
		{"unset port matches every vm on the ssh host", "", "f3.lan", 0, false, []string{"vm1", "vm2"}},
		{"explicit port matching no vm", "", "f3.lan", 2203, false, nil},
		{"explicit user differing", "", "paul@fishfinger.example", 0, false, nil},
		{"explicit port differing", "", "fishfinger.example", 22, false, nil},
		{"raw ssh args (-p or -o HostName=)", "", "r0.lan", 0, true, nil},
		{"unknown host", "", "stranger.example", 0, false, nil},
		{"substring is not a destination match", "", "r0.la", 0, false, nil},
		{"empty host", "", "", 0, false, nil},
		{"user only", "", "rex@", 0, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SelectionForDestination(tc.user, tc.host, tc.port, tc.extraSSH)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SelectionForDestination(%q, %q, %d, %v) = %v, want %v",
					tc.user, tc.host, tc.port, tc.extraSSH, got, tc.want)
			}
		})
	}
}

// TestSelectionForLocalHostname pins the local-run selection: exactly the
// names the hostname contains, case insensitive, and non-nil when empty.
func TestSelectionForLocalHostname(t *testing.T) {
	registerSelectionInventory(t)
	if got, want := SelectionForLocalHostname("PI10.lan.example"), []string{"pi1", "pi10"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pi10 selection = %v, want %v", got, want)
	}
	if got, want := SelectionForLocalHostname("r0-wg.example"), []string{"r0", "r0-wg"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("r0-wg selection = %v, want %v", got, want)
	}
	got := SelectionForLocalHostname("laptop")
	if got == nil || len(got) != 0 {
		t.Fatalf("unrelated hostname selection = %#v, want empty non-nil", got)
	}
}

// TestSelectionSameForEveryNameOfOneMachine pins that the selection depends
// on the machine, not on which of its names is pushed: db and pi10 share
// 10.0.0.5, and pi1 is contained in the alias pi10, so pi1 must be selected
// whether the push names db, pi10 or the raw SSH host. Regression: substring
// probes used to come from the targets only, so pushing db skipped pi1.
func TestSelectionSameForEveryNameOfOneMachine(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	mustAddHost(t, "db", func(h *Host) { h.SSHHost = "10.0.0.5" })
	mustAddHost(t, "pi10", func(h *Host) { h.SSHHost = "10.0.0.5" })
	mustAddHost(t, "pi1", func(h *Host) { h.SSHHost = "pi1.lan" })
	mustAddHost(t, "web", func(h *Host) { h.SSHHost = "web.lan" })

	want := []string{"db", "pi1", "pi10"}
	selections := map[string][]string{
		"SelectionForHosts(db)":             SelectionForHosts([]string{"db"}),
		"SelectionForHosts(pi10)":           SelectionForHosts([]string{"pi10"}),
		"SelectionForDestination(10.0.0.5)": SelectionForDestination("", "10.0.0.5", 0, false),
		"SelectionForDestination(db)":       SelectionForDestination("", "db", 0, false),
		"SelectionForDestination(pi10)":     SelectionForDestination("", "pi10", 0, false),
	}
	for call, got := range selections {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", call, got, want)
		}
	}
	// A cluster containing db but not pi10 still keeps pi1.
	got := SelectionForHosts([]string{"db", "web"})
	if want := []string{"db", "pi1", "pi10", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SelectionForHosts(db, web) = %v, want %v", got, want)
	}
}

// TestSelectionIgnoresSSHHostCase pins that SSH hosts compare
// case-insensitively like DNS names: db (Host5.lan) and pi10 (host5.lan) are
// one machine, so every name and every spelling of the destination yields the
// same selection, including pi1 contained in the alias pi10.
func TestSelectionIgnoresSSHHostCase(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	mustAddHost(t, "db", func(h *Host) { h.SSHHost = "Host5.lan" })
	mustAddHost(t, "pi10", func(h *Host) { h.SSHHost = "host5.lan" })
	mustAddHost(t, "pi1", func(h *Host) { h.SSHHost = "pi1.lan" })

	want := []string{"db", "pi1", "pi10"}
	selections := map[string][]string{
		"SelectionForHosts(db)":              SelectionForHosts([]string{"db"}),
		"SelectionForHosts(pi10)":            SelectionForHosts([]string{"pi10"}),
		"SelectionForDestination(Host5.lan)": SelectionForDestination("", "Host5.lan", 0, false),
		"SelectionForDestination(host5.lan)": SelectionForDestination("", "host5.lan", 0, false),
		"SelectionForDestination(HOST5.LAN)": SelectionForDestination("", "u@HOST5.LAN", 0, false),
		"SelectionForDestination(DB)":        SelectionForDestination("", "DB", 0, false),
	}
	for call, got := range selections {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", call, got, want)
		}
	}
}
