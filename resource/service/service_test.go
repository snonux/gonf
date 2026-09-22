package service

import (
	"os"
	"runtime"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/resource"
)

func TestDetectServiceManager(t *testing.T) {
	mgr, err := detectServiceManager()
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	switch runtime.GOOS {
	case "linux":
		if mgr != "systemd" {
			t.Fatalf("linux mgr = %q", mgr)
		}
	case "openbsd":
		if mgr != "rcctl" {
			t.Fatalf("openbsd mgr = %q", mgr)
		}
	case "freebsd":
		if mgr != "freebsd" {
			t.Fatalf("freebsd mgr = %q", mgr)
		}
	case "netbsd":
		if mgr != "netbsd" {
			t.Fatalf("netbsd mgr = %q", mgr)
		}
	}
}

func TestPresentIdempotentWithFakeRunner(t *testing.T) {
	resource.ResetRepository()

	switch runtime.GOOS {
	case "linux":
		testseam.FakeServiceRunner(t, fakeSystemdAlreadyOK)
	case "openbsd":
		testseam.FakeServiceRunner(t, fakeRcctlAlreadyOK)
	case "freebsd":
		testseam.FakeServiceRunner(t, fakeFreeBSDAlreadyOK)
	case "netbsd":
		testseam.FakeServiceRunner(t, fakeNetBSDAlreadyOK)
	default:
		t.Skip("unsupported GOOS")
	}

	Present("uptimed")
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
}

func TestWithRestartIssuesRestart(t *testing.T) {
	resource.ResetRepository()

	var sawRestart bool
	switch runtime.GOOS {
	case "linux":
		testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
			if name == "systemctl" && contains(args, "restart") {
				sawRestart = true
			}
			return fakeSystemdAlreadyOK(name, args...)
		})
	case "openbsd":
		testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
			if name == "rcctl" && len(args) > 0 && args[0] == "restart" {
				sawRestart = true
			}
			return fakeRcctlAlreadyOK(name, args...)
		})
	case "freebsd":
		testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
			if name == "service" && contains(args, "restart") {
				sawRestart = true
			}
			return fakeFreeBSDAlreadyOK(name, args...)
		})
	case "netbsd":
		testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
			if name == netbsdService && contains(args, "restart") {
				sawRestart = true
			}
			return fakeNetBSDAlreadyOK(name, args...)
		})
	default:
		t.Skip("unsupported GOOS")
	}

	Present("uptimed", opt.WithRestart)
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
	if !sawRestart {
		t.Fatal("expected restart action")
	}
}

func TestOnChangeGatesRestartButNotServiceConvergence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		watchStatus resource.Status
		wantRestart bool
	}{
		{name: "unchanged watch holds restart", watchStatus: resource.StatusOK},
		{name: "changed watch fires restart", watchStatus: resource.StatusChanged, wantRestart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			var sawRestart bool
			switch runtime.GOOS {
			case "linux":
				testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
					sawRestart = sawRestart || name == "systemctl" && contains(args, "restart")
					return fakeSystemdAlreadyOK(name, args...)
				})
			case "openbsd":
				testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
					sawRestart = sawRestart || name == "rcctl" && contains(args, "restart")
					return fakeRcctlAlreadyOK(name, args...)
				})
			case "freebsd":
				testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
					sawRestart = sawRestart || name == "service" && contains(args, "restart")
					return fakeFreeBSDAlreadyOK(name, args...)
				})
			case "netbsd":
				testseam.FakeServiceRunner(t, func(name string, args ...string) (string, string, int, error) {
					sawRestart = sawRestart || name == netbsdService && contains(args, "restart")
					return fakeNetBSDAlreadyOK(name, args...)
				})
			default:
				t.Skip("unsupported GOOS")
			}

			watched := testapply.Register("File", "unit", testapply.Noting(tc.watchStatus, "File[unit]"))
			Present("uptimed", opt.WithRestart, opt.OnChange(watched))
			if err := testapply.Apply(); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if sawRestart != tc.wantRestart {
				t.Fatalf("restart = %t, want %t", sawRestart, tc.wantRestart)
			}
		})
	}
}

func TestWithUserRejectedOnNonSystemd(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux uses systemd")
	}
	resource.ResetRepository()
	Present("uptimed", opt.WithUser)
	err := testapply.Apply()
	if err == nil {
		t.Fatal("expected WithUser error")
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func fakeSystemdAlreadyOK(name string, args ...string) (string, string, int, error) {
	if name != "systemctl" {
		return "", "", 1, nil
	}
	// is-active / is-enabled succeed; mutations should not be called in idempotent test
	if contains(args, "is-active") || contains(args, "is-enabled") {
		return "", "", 0, nil
	}
	if contains(args, "restart") || contains(args, "reload") {
		return "", "", 0, nil
	}
	return "", "unexpected systemctl " + join(args), 1, nil
}

func fakeRcctlAlreadyOK(name string, args ...string) (string, string, int, error) {
	if name != "rcctl" {
		return "", "", 1, nil
	}
	if len(args) >= 1 && args[0] == "check" {
		return "", "", 0, nil
	}
	if len(args) >= 3 && args[0] == "get" && args[2] == "status" {
		return "", "", 0, nil // enabled → exit 0 (stdout may be empty)
	}
	if len(args) >= 1 && (args[0] == "restart" || args[0] == "reload") {
		return "", "", 0, nil
	}
	return "", "unexpected rcctl " + join(args), 1, nil
}

func fakeFreeBSDAlreadyOK(name string, args ...string) (string, string, int, error) {
	if name != "service" {
		return "", "", 1, nil
	}
	if contains(args, "status") || contains(args, "enabled") {
		return "", "", 0, nil
	}
	if contains(args, "restart") || contains(args, "reload") {
		return "", "", 0, nil
	}
	return "", "unexpected service " + join(args), 1, nil
}

func fakeNetBSDAlreadyOK(name string, args ...string) (string, string, int, error) {
	if name != netbsdService {
		return "", "", 1, nil
	}
	if len(args) >= 1 && args[0] == "-e" {
		return "/etc/rc.d/uptimed\n", "", 0, nil
	}
	if contains(args, "status") {
		return "", "", 0, nil
	}
	if contains(args, "restart") || contains(args, "reload") {
		return "", "", 0, nil
	}
	return "", "unexpected service " + join(args), 1, nil
}

func join(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

// Live tests: cross-compile with GOOS=freebsd|openbsd, scp, run under doas.
//
//	GONF_RUN_BSD_SERVICE_TESTS=1 ./service.test -test.v -test.run LiveUptimed

func TestLiveUptimedPresent(t *testing.T) {
	if os.Getenv("GONF_RUN_BSD_SERVICE_TESTS") != "1" {
		t.Skip("set GONF_RUN_BSD_SERVICE_TESTS=1 for live service tests")
	}
	name := liveServiceName()
	resource.ResetRepository()
	Present(name)
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveUptimedRestart(t *testing.T) {
	if os.Getenv("GONF_RUN_BSD_SERVICE_TESTS") != "1" {
		t.Skip("set GONF_RUN_BSD_SERVICE_TESTS=1 for live service tests")
	}
	name := liveServiceName()
	resource.ResetRepository()
	Present(name, opt.WithRestart)
	if err := testapply.Apply(); err != nil {
		t.Fatal(err)
	}
}

func liveServiceName() string {
	if runtime.GOOS == "netbsd" {
		return "bozohttpd" // small daemon present on pi0.lan
	}
	return "uptimed"
}
