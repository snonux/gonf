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
// A first version of this test drove every kind's apply() end-to-end but,
// for file and dir, only ever through the "target does not exist yet"
// branch, and, for pkg and service, only ever through one backend (dnf, and
// whichever service manager the CI host actually has). That left several
// real, independently-guarded "if resource.DryRun()" checks structurally
// unreachable: file/checksum.go's and dir/dir.go's "already matches /
// already exists, but reapply attributes" branches, and the freebsd/netbsd/
// openbsd pkg backends and freebsd/netbsd/rcctl service backends. The
// dedicated *ReapplyAttrs subtests below now pre-create their target so that
// branch is the one exercised, and the *FreeBSD/*NetBSD/*OpenBSD/*Rcctl
// subtests force backend selection via SetDetectPackageManagerForTest (pkg,
// pre-existing) and SetDetectServiceManagerForTest (service, added for this
// fix) so every backend's own guard runs on a single host regardless of its
// actual GOOS. This only proves each backend's dry-run gate itself holds;
// it stubs the manager-detection and command-runner seams, so it cannot
// catch a bug specific to a real BSD binary's behavior that only that OS
// would exhibit.
//
// Each kind is its own subtest so a regression names exactly which kind (or
// which branch/backend of a kind) broke, and so kinds that require systemd
// (service/timer/daemon_reload/systemdtimer) can skip cleanly on a host
// without it instead of failing the whole suite.
func TestDryRunFitness(t *testing.T) {
	kinds := []struct {
		name string
		run  func(t *testing.T, tmp string)
	}{
		{"dir", dryRunDir},
		{"dir-reapply-attrs", dryRunDirReapplyAttrs},
		{"file", dryRunFile},
		{"file-reapply-attrs", dryRunFileReapplyAttrs},
		{"link", dryRunLink},
		{"cmd", dryRunCmd},
		{"cron", dryRunCron},
		{"pkg", dryRunPkg},
		{"pkg-freebsd", dryRunPkgFreeBSD},
		{"pkg-netbsd", dryRunPkgNetBSD},
		{"pkg-openbsd", dryRunPkgOpenBSD},
		{"service", dryRunService},
		{"service-freebsd", dryRunServiceFreeBSD},
		{"service-netbsd", dryRunServiceNetBSD},
		{"service-rcctl", dryRunServiceRcctl},
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

// dryRunDirReapplyAttrs exercises the OTHER branch of dir.go's
// ensureDirectorySelf: dryRunDir above always targets a brand-new directory,
// so it only ever reaches the os.IsNotExist branch. A directory that already
// exists takes the err == nil branch instead, which notes StatusOK and then
// falls all the way through to the unconditional applyAttributesTo
// chmod/chown at the bottom of the function -- guarded only by its own
// early "if resource.DryRun() { return nil }". This fixture pre-creates the
// directory with a mode that does not match what Dir.Present will apply, so
// a real chmod under a broken guard is observable as a mode change.
func dryRunDirReapplyAttrs(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "existingdir")
	const existingMode = os.FileMode(0o700)
	if err := os.Mkdir(path, existingMode); err != nil {
		t.Fatal(err)
	}
	// Pin the mode explicitly: os.Mkdir's mode argument is subject to the
	// process umask, so a stray umask could otherwise make existingMode not
	// actually land on disk.
	if err := os.Chmod(path, existingMode); err != nil {
		t.Fatal(err)
	}
	dir.Present(path, opt.WithMode(0o750))
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != existingMode {
		t.Fatalf("dry-run must not chmod %s (directory already existed): got mode %v, want unchanged %v", path, got, existingMode)
	}
}

func dryRunFile(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "newfile.txt")
	file.Present(path, opt.WithContent("hello"))
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, path)
}

// dryRunFileReapplyAttrs exercises the OTHER branch of file/checksum.go's
// ensureFile: dryRunFile above always targets a brand-new file, so it only
// ever reaches the "changed" branch that routes through resource.Mutate.
// This fixture pre-creates a file whose content already matches what
// File.Present will apply, but whose mode does not, so ensureFile's
// "!changed" branch is the one under test: it notes StatusOK and then, for
// anything that is not gated by dry-run, falls through to the unconditional
// f.applyAttributesTo chmod/chown -- guarded only by its own "if
// resource.DryRun() { return nil }" ahead of that call. A real chmod under a
// broken guard is observable as a mode change.
func dryRunFileReapplyAttrs(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "existing.txt")
	const content = "hello"
	const existingMode = os.FileMode(0o644)
	if err := os.WriteFile(path, []byte(content), existingMode); err != nil {
		t.Fatal(err)
	}
	// Pin the mode explicitly: os.WriteFile's mode argument is subject to the
	// process umask, so a stray umask could otherwise make existingMode not
	// actually land on disk.
	if err := os.Chmod(path, existingMode); err != nil {
		t.Fatal(err)
	}
	file.Present(path, opt.WithContent(content), opt.WithMode(0o600))
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != existingMode {
		t.Fatalf("dry-run must not chmod %s (content already matched): got mode %v, want unchanged %v", path, got, existingMode)
	}
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

// dryRunPkgBackend forces resource/pkg's package-manager detection to mgr
// (via the existing SetDetectPackageManagerForTest seam) and runs the same
// fixture as dryRunPkg against it, flagging a mutation the moment the
// backend issues a command isProbe does not recognize as its own read-only
// "is it installed" check. dryRunPkg above only ever forces "dnf", so the
// freebsd/netbsd/openbsd backends' own "if resource.DryRun()" guards
// (freebsd.go, netbsd.go, openbsd.go) were never reached by the fitness
// test even though the seam to reach them already existed.
func dryRunPkgBackend(t *testing.T, mgr string, isProbe func(name string, args []string) bool) {
	t.Helper()
	t.Cleanup(func() {
		pkg.ResetRunCmdForTest()
		pkg.ResetDetectPackageManagerForTest()
	})
	pkg.SetDetectPackageManagerForTest(func() (string, error) { return mgr, nil })
	var mutated bool
	pkg.SetRunCmdForTest(func(name string, args ...string) (string, string, int, error) {
		if isProbe(name, args) {
			// Not-installed probe response, so the backend decides an
			// install is needed and (absent its dry-run guard) would issue a
			// real mutating command next.
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
		t.Fatalf("dry-run must not run the package manager (%s backend)", mgr)
	}
}

func dryRunPkgFreeBSD(t *testing.T, tmp string) {
	dryRunPkgBackend(t, "freebsd", func(name string, args []string) bool {
		// freebsd.go probes with "pkg info -e NAME"; every other "pkg ..."
		// call (install/upgrade/remove) is a mutation.
		return name == "pkg" && len(args) > 0 && args[0] == "info"
	})
}

func dryRunPkgNetBSD(t *testing.T, tmp string) {
	dryRunPkgBackend(t, "netbsd", func(name string, args []string) bool {
		// netbsd.go probes via the pkg_info binary (netbsdPkgInfo, unexported
		// so its literal is duplicated here) and mutates via the separate
		// pkgin binary (netbsdPkgin) -- distinct binaries, so the probe is
		// identified by name alone.
		return name == "/usr/sbin/pkg_info"
	})
}

func dryRunPkgOpenBSD(t *testing.T, tmp string) {
	dryRunPkgBackend(t, "openbsd", func(name string, args []string) bool {
		// openbsd.go probes via pkg_info and mutates via pkg_add/pkg_delete
		// -- distinct binaries, so the probe is identified by name alone.
		return name == "pkg_info"
	})
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

// dryRunServiceBackend forces resource/service's service-manager detection
// to mgr (via SetDetectServiceManagerForTest) and probes the service as
// enabled-but-stopped, so a correctly-gated apply would queue exactly one
// "start" action and a broken guard would actually run it. classify
// inspects a runner call's args and reports "running" or "enabled" for a
// probe (answered not-running / enabled respectively) or "" for anything
// else, which flags a mutation. Before SetDetectServiceManagerForTest
// existed, the freebsd/netbsd/rcctl backends (each with their own "if
// resource.DryRun()" guard) were only reachable by actually running the
// fitness test on that OS, so this seam and these subtests are what makes
// them testable on a single Linux CI host.
func dryRunServiceBackend(t *testing.T, mgr string, classify func(args []string) string) {
	t.Helper()
	t.Cleanup(func() {
		service.ResetRunCmdForTest()
		service.ResetDetectServiceManagerForTest()
	})
	service.SetDetectServiceManagerForTest(func() (string, error) { return mgr, nil })
	var mutated bool
	service.SetRunCmdForTest(func(name string, args ...string) (string, string, int, error) {
		switch classify(args) {
		case "running":
			return "", "", 1, nil // not running
		case "enabled":
			return "", "", 0, nil // enabled
		default:
			mutated = true
			return "", "", 0, nil
		}
	})
	service.Present("fit-service")
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatalf("dry-run must not run the service manager (%s backend)", mgr)
	}
}

func dryRunServiceFreeBSD(t *testing.T, tmp string) {
	dryRunServiceBackend(t, "freebsd", func(args []string) string {
		// freebsd.go probes with "service NAME status"/"service NAME enabled".
		if len(args) == 2 && args[1] == "status" {
			return "running"
		}
		if len(args) == 2 && args[1] == "enabled" {
			return "enabled"
		}
		return ""
	})
}

func dryRunServiceNetBSD(t *testing.T, tmp string) {
	dryRunServiceBackend(t, "netbsd", func(args []string) string {
		// netbsd.go probes with "service NAME status" and "service -e NAME".
		// enabled=true here deliberately avoids ever exercising the
		// enable/disable action, which writes to netbsdRcConfD (default
		// /etc/rc.conf.d) directly instead of through this runner seam.
		if len(args) == 2 && args[1] == "status" {
			return "running"
		}
		if len(args) == 2 && args[0] == "-e" {
			return "enabled"
		}
		return ""
	})
}

func dryRunServiceRcctl(t *testing.T, tmp string) {
	dryRunServiceBackend(t, "rcctl", func(args []string) string {
		// rcctl.go probes with "rcctl check NAME" and "rcctl get NAME status".
		if len(args) >= 2 && args[0] == "check" {
			return "running"
		}
		if len(args) >= 2 && args[0] == "get" {
			return "enabled"
		}
		return ""
	})
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
