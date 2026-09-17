package resource_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	iexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/pkg"
	"github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/systemdtimer"
	"github.com/snonux/gonf/resource/timer"
)

// TestDryRunFitness is the safety net the "centralize dry-run handling"
// cleanup (t5) exists for: it applies EVERY resource/* kind with dry-run on
// and every command/exec runner stubbed to fail the test the instant a
// mutating call is attempted, and it also asserts no real filesystem
// mutation landed on disk. A resource kind that forgets its dry-run gate —
// whether it routes through the new resource.Mutate helper or still has its
// own ad-hoc "if resource.DryRun()" check — mutates for real here and this
// test catches it, regardless of which style the kind uses internally.
//
// Each kind is its own subtest so a regression names exactly which kind
// broke, and so kinds that require systemd (service/timer/daemon_reload/
// systemdtimer) can skip cleanly on a host without it instead of failing the
// whole suite.
func TestDryRunFitness(t *testing.T) {
	kinds := []struct {
		name string
		run  func(t *testing.T, tmp string)
	}{
		{"dir", dryRunDir},
		{"file", dryRunFile},
		{"link", dryRunLink},
		{"cmd", dryRunCmd},
		{"cron", dryRunCron},
		{"pkg", dryRunPkg},
		{"service", dryRunService},
		{"daemon_reload", dryRunDaemonReload},
		{"timer", dryRunTimer},
		{"systemdtimer", dryRunSystemdTimer},
	}

	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			resource.SetDryRun(true)
			k.run(t, t.TempDir())
		})
	}
}

// requireSystemd skips t when systemd unit management is not available on
// this host (mirrors the skip convention already used by
// resource/timer/timer_test.go).
func requireSystemd(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd resources are Linux-only")
	}
	if !systemd.Detected() {
		t.Skip("systemd not detected on this host")
	}
}

// fakeSystemctlRunner answers systemctl is-active/is-enabled queries with
// "inactive"/"disabled" (exit 1, no error — a normal negative per
// resource/systemd's client.go) so the caller's convergence logic decides a
// mutation is needed, then flags *mutated if anything else (enable, start,
// restart, disable, stop, daemon-reload, ...) is actually invoked. Under a
// correct dry-run gate that second call must never happen.
func fakeSystemctlRunner(mutated *bool) func(name string, args ...string) (string, string, int, error) {
	return func(name string, args ...string) (string, string, int, error) {
		for _, a := range args {
			if a == "is-active" || a == "is-enabled" {
				return "", "", 1, nil
			}
		}
		*mutated = true
		return "", "", 0, nil
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not touch %s (Lstat err=%v)", path, err)
	}
}

func dryRunDir(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "newdir")
	dir.Present(path)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, path)
}

func dryRunFile(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "newfile.txt")
	file.Present(path, opt.WithContent("hello"))
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, path)
}

func dryRunLink(t *testing.T, tmp string) {
	target := filepath.Join(tmp, "target")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "link")
	link.Present(path, opt.WithSymlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, path)
}

func dryRunCmd(t *testing.T, tmp string) {
	t.Cleanup(cmd.ResetRunnersForTest)
	var mutated bool
	cmd.SetRunnersForTest(
		func(opts iexec.Opts, name string, args ...string) (string, string, int, error) {
			mutated = true
			return "", "", 0, nil
		},
		func(name string, args ...string) (string, string, int, error) {
			// Guard probes (Unless/OnlyIf) are not used by this fixture, but
			// keep the seam wired so a future probe call is caught too.
			mutated = true
			return "", "", 0, nil
		},
	)
	cmd.Present("true", nil)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not execute the command")
	}
}

func dryRunCron(t *testing.T, tmp string) {
	t.Cleanup(cron.ResetRunnersForTest)
	var mutated bool
	cron.SetRunnersForTest(
		func(name string, args ...string) (string, string, int, error) {
			// crontab -l on an empty crontab: exit 0, empty stdout.
			return "", "", 0, nil
		},
		func(stdin, name string, args ...string) (string, string, int, error) {
			mutated = true
			return "", "", 0, nil
		},
	)
	cron.Present("fit-job", opt.WithCommand("true"))
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not write the crontab")
	}
}

func dryRunPkg(t *testing.T, tmp string) {
	t.Cleanup(func() {
		pkg.ResetRunCmdForTest()
		pkg.ResetDetectPackageManagerForTest()
	})
	// Force a deterministic backend regardless of the host OS/distro so this
	// subtest is portable.
	pkg.SetDetectPackageManagerForTest(func() (string, error) { return "dnf", nil })
	var mutated bool
	pkg.SetRunCmdForTest(func(name string, args ...string) (string, string, int, error) {
		if name == "rpm" {
			// rpm -q is a read-only probe: report "not installed" so the
			// backend decides a package install is needed.
			return "", "not installed", 1, nil
		}
		mutated = true
		return "", "", 0, nil
	})
	pkg.Present("fit-pkg")
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run the package manager")
	}
}

func dryRunService(t *testing.T, tmp string) {
	requireSystemd(t)
	t.Cleanup(service.ResetRunCmdForTest)
	var mutated bool
	service.SetRunCmdForTest(fakeSystemctlRunner(&mutated))
	service.Present("fit-service")
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run systemctl for the service")
	}
}

func dryRunDaemonReload(t *testing.T, tmp string) {
	requireSystemd(t)
	t.Cleanup(systemd.ResetRunCmdForTest)
	var mutated bool
	systemd.SetRunCmdForTest(fakeSystemctlRunner(&mutated))
	systemd.Present()
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run systemctl daemon-reload")
	}
}

func dryRunTimer(t *testing.T, tmp string) {
	requireSystemd(t)
	t.Cleanup(systemd.ResetRunCmdForTest)
	var mutated bool
	systemd.SetRunCmdForTest(fakeSystemctlRunner(&mutated))
	timer.Present("fit-timer")
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run systemctl for the timer")
	}
}

func dryRunSystemdTimer(t *testing.T, tmp string) {
	requireSystemd(t)
	// WithUser + a HOME override keeps this fixture inside the temp dir even
	// if the dry-run gate has a bug: the unit dir is derived from
	// os.UserHomeDir(), never a hardcoded /etc path, when WithUser is set.
	t.Setenv("HOME", tmp)
	t.Cleanup(systemd.ResetRunCmdForTest)
	var mutated bool
	systemd.SetRunCmdForTest(fakeSystemctlRunner(&mutated))
	systemdtimer.Present("fit-systemdtimer",
		opt.WithUser,
		opt.WithCommand("/bin/true"),
		opt.WithOnCalendar("*-*-* *:00:00"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run systemctl for the systemd timer")
	}
	unitDir := filepath.Join(tmp, ".config", "systemd", "user")
	assertAbsent(t, unitDir)
	assertAbsent(t, filepath.Join(unitDir, "fit-systemdtimer.service"))
	assertAbsent(t, filepath.Join(unitDir, "fit-systemdtimer.timer"))
}
