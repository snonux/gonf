package inventory

import (
	"fmt"
	"sync"
	"testing"

	inv "github.com/snonux/gonf/internal/inventory"
)

// sshHostOf returns the registered SSHHost of name via the public listing.
func sshHostOf(t *testing.T, name string) string {
	t.Helper()
	for _, h := range Hosts() {
		if h.Name == name {
			return h.SSHHost
		}
	}
	t.Fatalf("host %q not registered", name)
	return ""
}

// TestSSHHostIndependentOfOptionOrder pins task 9b: the SSH hostname is
// derived once every option ran, so an explicit WithSSHHost wins whichever
// order it comes in (even when it equals the inventory name), and the last
// WithSSHDomain wins, bundle or not.
func TestSSHHostIndependentOfOptionOrder(t *testing.T) {
	ResetInventory()
	t.Cleanup(ResetInventory)
	lan := HostDefaults(WithSSHDomain("lan"))
	cases := []struct {
		name string
		opts []HostOption
		want string
	}{
		{"plain", nil, "plain"},
		{"bundle", []HostOption{lan}, "bundle.lan"},
		{"domain-override", []HostOption{lan, WithSSHDomain("wg0")}, "domain-override.wg0"},
		{"domain-twice", []HostOption{WithSSHDomain("lan"), WithSSHDomain(".wg0.")}, "domain-twice.wg0"},
		{"nested-bundles", []HostOption{HostDefaults(lan, WithSSHDomain("wg0"))}, "nested-bundles.wg0"},
		{"explicit-name-first", []HostOption{WithSSHHost("explicit-name-first"), lan}, "explicit-name-first"},
		{"explicit-first", []HostOption{WithSSHHost("x.example"), lan}, "x.example"},
		{"explicit-last", []HostOption{lan, WithSSHHost("y.example")}, "y.example"},
		{"explicit-in-bundle", []HostOption{HostDefaults(WithSSHHost("z.example")), WithSSHDomain("wg0")}, "z.example"},
		{"explicit-twice", []HostOption{WithSSHHost("a.example"), WithSSHHost("b.example")}, "b.example"},
		{"explicit-cleared", []HostOption{WithSSHHost("a.example"), lan, WithSSHHost("")}, "explicit-cleared.lan"},
	}
	for _, tc := range cases {
		Host(tc.name, tc.opts...)
		if got := sshHostOf(t, tc.name); got != tc.want {
			t.Errorf("%s: SSHHost = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestWithSSHDomainRejectsEmptyDomain is the negative case: a domain that is
// empty after trimming dots refuses the host, also from inside a bundle and
// even when a valid domain or explicit host would otherwise decide.
func TestWithSSHDomainRejectsEmptyDomain(t *testing.T) {
	ResetInventory()
	t.Cleanup(ResetInventory)
	const want = "WithSSHDomain: domain must not be empty"
	cases := map[string][]HostOption{
		"empty":       {WithSSHDomain("")},
		"dots":        {WithSSHDomain("..")},
		"in-bundle":   {HostDefaults(WithSSHDomain("."))},
		"before-good": {WithSSHDomain("."), WithSSHDomain("lan")},
		"explicit":    {WithSSHHost("x.example"), WithSSHDomain("")},
	}
	for name, opts := range cases {
		_, err := inv.AddHost(name, opts...)
		if err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
		if _, ok := LookupHost(name); ok {
			t.Errorf("%s: refused host was registered", name)
		}
	}
}

// TestSharedSSHDomainBundleConcurrent pins task db: a WithSSHDomain option
// shared by concurrent Host calls (registration is documented safe for
// concurrent use) only reads its captured domain. Run with -race; the old
// closure assigned the captured parameter and raced here.
func TestSharedSSHDomainBundleConcurrent(t *testing.T) {
	ResetInventory()
	t.Cleanup(ResetInventory)
	lan := HostDefaults(WithSSHDomain("lan."))
	const n = 32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Host(fmt.Sprintf("h%d", i), lan)
		}()
	}
	wg.Wait()
	for i := range n {
		name := fmt.Sprintf("h%d", i)
		if got, want := sshHostOf(t, name), name+".lan"; got != want {
			t.Errorf("%s: SSHHost = %q, want %q", name, got, want)
		}
	}
}
