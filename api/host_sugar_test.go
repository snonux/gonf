package api

import (
	"testing"

	"github.com/snonux/gonf/internal/inventory"
)

func TestWithSSHDomainAndPlatform(t *testing.T) {
	ResetInventory()
	t.Cleanup(ResetInventory)
	lan := HostDefaults(WithSSHDomain("lan.example."), WithPlatform("freebsd/arm64"))
	Host("f0", lan)
	Host("f1", WithSSHHost("other.example"), lan)
	Host("f2", lan, WithSSHHost("explicit.example"))

	for name, want := range map[string]string{
		"f0": "f0.lan.example",
		"f1": "other.example",
		"f2": "explicit.example",
	} {
		rec, ok := inventory.LookupHost(name)
		if !ok {
			t.Fatalf("host %s not registered", name)
		}
		if rec.SSHHost != want {
			t.Errorf("%s SSHHost = %q, want %q", name, rec.SSHHost, want)
		}
		if rec.GOOS != "freebsd" || rec.GOARCH != "arm64" {
			t.Errorf("%s platform = %s/%s, want freebsd/arm64", name, rec.GOOS, rec.GOARCH)
		}
	}
}

func TestWithSSHDomainAndPlatformMisuse(t *testing.T) {
	requireDeclErr(t, "WithSSHDomain: domain must not be empty", func() { Host("x", WithSSHDomain(".")) })
	requireDeclErr(t, `WithPlatform("linux"): want "goos/goarch", e.g. "linux/amd64"`, func() { Host("x", WithPlatform("linux")) })
	requireDeclErr(t, `WithPlatform("plan9/amd64"): unsupported GOOS "plan9"`, func() { Host("x", WithPlatform("plan9/amd64")) })
}
