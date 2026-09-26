package inventory

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	inv "github.com/snonux/gonf/internal/inventory"
	"github.com/snonux/gonf/internal/platform"
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

// TestWithGOOSAndPlatformAcceptEverySupportedGOOS pins task cb: both options
// validate against internal/platform, so every managed GOOS is accepted.
func TestWithGOOSAndPlatformAcceptEverySupportedGOOS(t *testing.T) {
	ResetInventory()
	t.Cleanup(ResetInventory)
	for _, goos := range platform.Supported() {
		for name, opt := range map[string]HostOption{
			"goos-" + goos:     WithGOOS(goos),
			"platform-" + goos: WithPlatform(goos + "/amd64"),
		} {
			if _, err := inv.AddHost(name, opt); err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			rec, _ := inv.LookupHost(name)
			if rec.GOOS != goos {
				t.Errorf("%s: GOOS = %q, want %q", name, rec.GOOS, goos)
			}
		}
	}
}

// TestWithGOOSAndPlatformRefuseUnsupported is the negative case: an unknown,
// empty or wrongly cased GOOS refuses the host with a clear error, also from
// inside a bundle.
func TestWithGOOSAndPlatformRefuseUnsupported(t *testing.T) {
	ResetInventory()
	t.Cleanup(ResetInventory)
	cases := map[string]struct {
		opt  HostOption
		want string
	}{
		"goos-unknown":      {WithGOOS("plan9"), `WithGOOS: unsupported GOOS "plan9" (want one of linux, darwin, freebsd, openbsd, netbsd)`},
		"goos-empty":        {WithGOOS(""), `WithGOOS: empty GOOS (want one of linux, darwin, freebsd, openbsd, netbsd)`},
		"goos-case":         {WithGOOS("Linux"), `WithGOOS: unsupported GOOS "Linux" (GOOS names are lower case: did you mean "linux"?)`},
		"goos-bundle":       {HostDefaults(WithGOOS("FreeBSD")), `WithGOOS: unsupported GOOS "FreeBSD" (GOOS names are lower case: did you mean "freebsd"?)`},
		"platform-unknown":  {WithPlatform("windows/amd64"), `WithPlatform("windows/amd64"): unsupported GOOS "windows" (want one of`},
		"platform-empty-os": {WithPlatform("/amd64"), `WithPlatform("/amd64"): empty GOOS (want one of`},
		"platform-case":     {WithPlatform("OpenBSD/amd64"), `WithPlatform("OpenBSD/amd64"): unsupported GOOS "OpenBSD" (GOOS names are lower case: did you mean "openbsd"?)`},
	}
	for name, tc := range cases {
		_, err := inv.AddHost(name, tc.opt)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want prefix %q", name, err, tc.want)
		}
		if _, ok := LookupHost(name); ok {
			t.Errorf("%s: refused host was registered", name)
		}
	}
}
