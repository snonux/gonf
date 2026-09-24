package pkg

import (
	"os"
	"runtime"
	"testing"

	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
)

func TestDetectPackageManager(t *testing.T) {
	mgr, err := detectPackageManager()
	switch runtime.GOOS {
	case "linux":
		if err != nil {
			t.Skip(err) // non-dnf linux
		}
		if mgr != "dnf" {
			t.Fatalf("mgr = %q", mgr)
		}
	case "openbsd", "freebsd", "netbsd":
		if err != nil {
			t.Fatal(err)
		}
		if mgr != runtime.GOOS {
			t.Fatalf("mgr = %q, want %s", mgr, runtime.GOOS)
		}
	default:
		if err == nil {
			t.Fatalf("expected error on %s", runtime.GOOS)
		}
	}
}

func TestPresentIdempotentFake(t *testing.T) {
	resource.ResetRepository()

	var run func(string, ...string) (string, string, int, error)
	switch runtime.GOOS {
	case "openbsd":
		run = fakeOpenBSDInstalled
	case "freebsd":
		run = fakeFreeBSDInstalled
	case "netbsd":
		run = fakeNetBSDInstalled
	default:
		t.Skip("no fake for this OS")
	}

	Present("rsync")
	if err := testapply.ApplyWithRunners(&runners.Set{Package: &runners.PackageRunners{Run: run}}); err != nil {
		t.Fatal(err)
	}
}

func fakeOpenBSDInstalled(name string, args ...string) (string, string, int, error) {
	if name == "pkg_info" && len(args) >= 2 && args[0] == "-e" {
		return "inst:rsync-1\n", "", 0, nil
	}
	return "", "unexpected " + name, 1, nil
}

func fakeFreeBSDInstalled(name string, args ...string) (string, string, int, error) {
	if name == "pkg" && len(args) >= 2 && args[0] == "info" && args[1] == "-e" {
		return "", "", 0, nil
	}
	return "", "unexpected " + name, 1, nil
}

func fakeNetBSDInstalled(name string, args ...string) (string, string, int, error) {
	if name == netbsdPkgInfo && len(args) >= 2 && args[0] == "-e" {
		return "rsync-1\n", "", 0, nil
	}
	return "", "unexpected " + name, 1, nil
}

// Live package probe: already-installed rsync should be StatusOK / no error.
func TestLiveRsyncPresent(t *testing.T) {
	if os.Getenv("GONF_RUN_BSD_PACKAGE_TESTS") != "1" {
		t.Skip("set GONF_RUN_BSD_PACKAGE_TESTS=1 for live package tests")
	}
	resource.ResetRepository()
	Present("rsync")
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
}
