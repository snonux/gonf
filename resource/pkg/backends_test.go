package pkg

import (
	"errors"
	"slices"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

// pkgCall records one invocation of the swapped runCmd.
type pkgCall struct {
	bin  string
	args []string
}

// fakePkgRunner fakes a package-manager probe-then-act flow: an invocation
// carrying a -e flag (or rpm -q) is the installed-state probe and exits 0 or
// 1 per installed; every other invocation is an action, recorded and
// successful (exit 3 when failAction is set, a start error when actionErr is
// set). probeErr makes the probe fail to start.
func fakePkgRunner(installed, probeErr, failAction bool, calls *[]pkgCall) func(string, ...string) (string, string, int, error) {
	return func(bin string, args ...string) (string, string, int, error) {
		*calls = append(*calls, pkgCall{bin: bin, args: args})
		probe := slices.Contains(args, "-e") ||
			(bin == "rpm" && len(args) == 2 && args[0] == "-q")
		if probe {
			if probeErr {
				return "", "", -1, errors.New("exec: probe not found")
			}
			if installed {
				return "", "", 0, nil
			}
			return "", "not installed\n", 1, nil
		}
		if failAction {
			return "out\n", "err\n", 3, nil
		}
		return "", "", 0, nil
	}
}

// assertPkgAction checks the recorded invocations: exactly one probe first,
// then at most one action matching wantRun (binary + args; nil means no
// action may run).
func assertPkgAction(t *testing.T, probe pkgCall, wantRun []string, calls []pkgCall) {
	t.Helper()
	if len(calls) == 0 || calls[0].bin != probe.bin || !slices.Equal(calls[0].args, probe.args) {
		t.Fatalf("first invocation = %v, want probe %v", calls, probe)
	}
	if wantRun == nil {
		if len(calls) != 1 {
			t.Errorf("expected only the probe, calls: %v", calls)
		}
		return
	}
	if len(calls) != 2 {
		t.Fatalf("expected probe + one action, calls: %v", calls)
	}
	if calls[1].bin != wantRun[0] || !slices.Equal(calls[1].args, wantRun[1:]) {
		t.Errorf("action = %v %v, want %v", calls[1].bin, calls[1].args, wantRun)
	}
}

// TestApplyFreeBSDPkgFake pins the probe-then-act behavior of the FreeBSD pkg
// backend, mirroring the dnf backend: converged states note ok without
// running pkg, would-act states run pkg and note changed (would-change in
// dry-run).
func TestApplyFreeBSDPkgFake(t *testing.T) {
	oldRun, oldDry := runCmd, resource.DryRun()
	defer func() {
		runCmd = oldRun
		resource.SetDryRun(oldDry)
	}()

	withAbsent := func(p Package) Package { p.Absent = true; return p }
	withLatest := func(p Package) Package { p.latest = true; return p }

	tests := []struct {
		name       string
		pkg        Package
		installed  bool
		probeErr   bool
		failAction bool
		dryRun     bool
		wantRun    []string // expected invocation (binary + args); nil = no action
		wantNote   resource.Status
		wantErr    string
	}{
		{
			name:      "present converges when pkg reports installed",
			pkg:       Package{name: "rsync"},
			installed: true,
			wantNote:  resource.StatusOK,
		},
		{
			name:     "present installs when pkg reports not installed",
			pkg:      Package{name: "rsync"},
			wantRun:  []string{"pkg", "install", "-y", "rsync"},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "absent converges when pkg reports not installed",
			pkg:      withAbsent(Package{name: "rsync"}),
			wantNote: resource.StatusOK,
		},
		{
			name:      "absent removes when pkg reports installed",
			pkg:       withAbsent(Package{name: "rsync"}),
			installed: true,
			wantRun:   []string{"pkg", "remove", "-y", "rsync"},
			wantNote:  resource.StatusChanged,
		},
		{
			name:     "latest upgrades when not installed",
			pkg:      withLatest(Package{name: "rsync"}),
			wantRun:  []string{"pkg", "upgrade", "-y", "rsync"},
			wantNote: resource.StatusChanged,
		},
		{
			name:      "latest upgrades even when installed",
			pkg:       withLatest(Package{name: "rsync"}),
			installed: true,
			wantRun:   []string{"pkg", "upgrade", "-y", "rsync"},
			wantNote:  resource.StatusChanged,
		},
		{
			name:     "dry-run on would-act notes would-change without pkg",
			pkg:      Package{name: "rsync"},
			dryRun:   true,
			wantNote: resource.StatusWouldChange,
		},
		{
			name:      "dry-run on converged path stays ok",
			pkg:       withAbsent(Package{name: "rsync"}),
			installed: false,
			dryRun:    true,
			wantNote:  resource.StatusOK,
		},
		{
			name:     "probe start failure is an error",
			pkg:      Package{name: "rsync"},
			probeErr: true,
			wantErr:  "pkg info -e rsync",
		},
		{
			name:       "action exit failure is an error",
			pkg:        Package{name: "rsync"},
			failAction: true,
			wantRun:    []string{"pkg", "install", "-y", "rsync"},
			wantErr:    "pkg [install -y rsync] failed (exit 3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)

			var calls []pkgCall
			runCmd = fakePkgRunner(tt.installed, tt.probeErr, tt.failAction, &calls)

			err := applyVia(freebsdBackend{})(&tt.pkg)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("freebsd apply err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("freebsd apply: %v", err)
			}

			probe := pkgCall{bin: "pkg", args: []string{"info", "-e", tt.pkg.name}}
			assertPkgAction(t, probe, tt.wantRun, calls)
			assertPkgNote(t, tt.pkg.name, tt.wantNote)
		})
	}
}

// TestApplyNetBSDFake pins the pkgin backend: the pkg_info -e probe gates the
// action, latest always runs pkgin install (it upgrades when newer is
// available), and all invocations route through runCmd.
func TestApplyNetBSDFake(t *testing.T) {
	oldRun, oldDry := runCmd, resource.DryRun()
	defer func() {
		runCmd = oldRun
		resource.SetDryRun(oldDry)
	}()

	withAbsent := func(p Package) Package { p.Absent = true; return p }
	withLatest := func(p Package) Package { p.latest = true; return p }

	tests := []struct {
		name       string
		pkg        Package
		installed  bool
		probeErr   bool
		failAction bool
		dryRun     bool
		wantRun    []string
		wantNote   resource.Status
		wantErr    string
	}{
		{
			name:      "present converges when pkg_info reports installed",
			pkg:       Package{name: "rsync"},
			installed: true,
			wantNote:  resource.StatusOK,
		},
		{
			name:     "present installs when pkg_info reports not installed",
			pkg:      Package{name: "rsync"},
			wantRun:  []string{netbsdPkgin, "-y", "install", "rsync"},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "absent converges when pkg_info reports not installed",
			pkg:      withAbsent(Package{name: "rsync"}),
			wantNote: resource.StatusOK,
		},
		{
			name:      "absent removes when pkg_info reports installed",
			pkg:       withAbsent(Package{name: "rsync"}),
			installed: true,
			wantRun:   []string{netbsdPkgin, "-y", "remove", "rsync"},
			wantNote:  resource.StatusChanged,
		},
		{
			name:     "latest installs (upgrades) when not installed",
			pkg:      withLatest(Package{name: "rsync"}),
			wantRun:  []string{netbsdPkgin, "-y", "install", "rsync"},
			wantNote: resource.StatusChanged,
		},
		{
			name:      "latest installs (upgrades) even when installed",
			pkg:       withLatest(Package{name: "rsync"}),
			installed: true,
			wantRun:   []string{netbsdPkgin, "-y", "install", "rsync"},
			wantNote:  resource.StatusChanged,
		},
		{
			name:     "dry-run on would-act notes would-change without pkgin",
			pkg:      Package{name: "rsync"},
			dryRun:   true,
			wantNote: resource.StatusWouldChange,
		},
		{
			name:      "dry-run on converged path stays ok",
			pkg:       withAbsent(Package{name: "rsync"}),
			installed: false,
			dryRun:    true,
			wantNote:  resource.StatusOK,
		},
		{
			name:     "probe start failure is an error",
			pkg:      Package{name: "rsync"},
			probeErr: true,
			wantErr:  "pkg_info -e rsync",
		},
		{
			name:       "action exit failure is an error",
			pkg:        Package{name: "rsync"},
			failAction: true,
			wantRun:    []string{netbsdPkgin, "-y", "install", "rsync"},
			wantErr:    netbsdPkgin + " [-y install rsync] failed (exit 3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)

			var calls []pkgCall
			runCmd = fakePkgRunner(tt.installed, tt.probeErr, tt.failAction, &calls)

			err := applyVia(netbsdBackend{})(&tt.pkg)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("netbsd apply err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("netbsd apply: %v", err)
			}

			probe := pkgCall{bin: netbsdPkgInfo, args: []string{"-e", tt.pkg.name}}
			assertPkgAction(t, probe, tt.wantRun, calls)
			assertPkgNote(t, tt.pkg.name, tt.wantNote)
		})
	}
}

// TestApplyOpenBSDFake pins the pkg_add/pkg_delete backend: the pkgspec stem
// probe (name-*) gates the action, and absent removals dry-run through the
// pkg_delete branch.
func TestApplyOpenBSDFake(t *testing.T) {
	oldRun, oldDry := runCmd, resource.DryRun()
	defer func() {
		runCmd = oldRun
		resource.SetDryRun(oldDry)
	}()

	withAbsent := func(p Package) Package { p.Absent = true; return p }
	withLatest := func(p Package) Package { p.latest = true; return p }

	tests := []struct {
		name       string
		pkg        Package
		installed  bool
		probeErr   bool
		failAction bool
		dryRun     bool
		wantRun    []string
		wantNote   resource.Status
		wantErr    string
	}{
		{
			name:      "present converges when pkg_info reports installed",
			pkg:       Package{name: "rsync"},
			installed: true,
			wantNote:  resource.StatusOK,
		},
		{
			name:     "present pkg_adds when pkg_info reports not installed",
			pkg:      Package{name: "rsync"},
			wantRun:  []string{"pkg_add", "rsync"},
			wantNote: resource.StatusChanged,
		},
		{
			name:     "absent converges when pkg_info reports not installed",
			pkg:      withAbsent(Package{name: "rsync"}),
			wantNote: resource.StatusOK,
		},
		{
			name:      "absent pkg_deletes when pkg_info reports installed",
			pkg:       withAbsent(Package{name: "rsync"}),
			installed: true,
			wantRun:   []string{"pkg_delete", "rsync"},
			wantNote:  resource.StatusChanged,
		},
		{
			name:     "latest installs when not installed",
			pkg:      withLatest(Package{name: "rsync"}),
			wantRun:  []string{"pkg_add", "rsync"},
			wantNote: resource.StatusChanged,
		},
		{
			name:      "latest upgrades even when installed",
			pkg:       withLatest(Package{name: "rsync"}),
			installed: true,
			wantRun:   []string{"pkg_add", "-u", "rsync"},
			wantNote:  resource.StatusChanged,
		},
		{
			name:     "dry-run on install path notes would-change without pkg_add",
			pkg:      Package{name: "rsync"},
			dryRun:   true,
			wantNote: resource.StatusWouldChange,
		},
		{
			name:      "dry-run on absent removal notes would-change via pkg_delete",
			pkg:       withAbsent(Package{name: "rsync"}),
			installed: true,
			dryRun:    true,
			wantNote:  resource.StatusWouldChange,
		},
		{
			name:      "dry-run on converged path stays ok",
			pkg:       withAbsent(Package{name: "rsync"}),
			installed: false,
			dryRun:    true,
			wantNote:  resource.StatusOK,
		},
		{
			name:       "absent removal exit failure is an error",
			pkg:        withAbsent(Package{name: "rsync"}),
			installed:  true,
			failAction: true,
			wantRun:    []string{"pkg_delete", "rsync"},
			wantErr:    "pkg_delete [rsync] failed (exit 3)",
		},
		{
			name:     "probe start failure is an error",
			pkg:      Package{name: "rsync"},
			probeErr: true,
			wantErr:  "pkg_info -e rsync-*",
		},
		{
			name:       "action exit failure is an error",
			pkg:        Package{name: "rsync"},
			failAction: true,
			wantRun:    []string{"pkg_add", "rsync"},
			wantErr:    "pkg_add [rsync] failed (exit 3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)

			var calls []pkgCall
			runCmd = fakePkgRunner(tt.installed, tt.probeErr, tt.failAction, &calls)

			err := applyVia(openbsdBackend{})(&tt.pkg)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("openbsd apply err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("openbsd apply: %v", err)
			}

			probe := pkgCall{bin: "pkg_info", args: []string{"-e", tt.pkg.name + "-*"}}
			assertPkgAction(t, probe, tt.wantRun, calls)
			assertPkgNote(t, tt.pkg.name, tt.wantNote)
		})
	}
}

// TestApplyDispatchesToDetectedBackend pins that Package.apply routes to the
// backend named by the swapped detector: each backend's probe binary runs
// first, nothing else.
func TestApplyDispatchesToDetectedBackend(t *testing.T) {
	oldRun := runCmd
	defer func() {
		ResetDetectPackageManagerForTest()
		runCmd = oldRun
	}()

	tests := []struct {
		mgr       string
		wantProbe string
	}{
		{"dnf", "rpm"},
		{"freebsd", "pkg"},
		{"netbsd", netbsdPkgInfo},
		{"openbsd", "pkg_info"},
	}

	for _, tt := range tests {
		t.Run(tt.mgr, func(t *testing.T) {
			resource.ResetReport()
			SetDetectPackageManagerForTest(func() (string, error) { return tt.mgr, nil })
			defer ResetDetectPackageManagerForTest()

			var calls []pkgCall
			runCmd = fakePkgRunner(true, false, false, &calls)

			p := Package{name: "rsync"}
			if err := p.apply(); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if len(calls) == 0 || calls[0].bin != tt.wantProbe {
				t.Fatalf("first invocation = %v, want probe via %s", calls, tt.wantProbe)
			}
			assertPkgNote(t, "rsync", resource.StatusOK)
		})
	}
}

// TestApplyErrorsWithoutPackageManager covers the detector-error and
// unsupported-manager branches of apply.
func TestApplyErrorsWithoutPackageManager(t *testing.T) {
	oldRun := runCmd
	defer func() {
		ResetDetectPackageManagerForTest()
		runCmd = oldRun
	}()

	var calls []pkgCall
	runCmd = fakePkgRunner(false, false, false, &calls)

	SetDetectPackageManagerForTest(func() (string, error) { return "", errors.New("no package manager here") })
	if err := (&Package{name: "rsync"}).apply(); err == nil {
		t.Fatal("expected the detector error to surface")
	}

	SetDetectPackageManagerForTest(func() (string, error) { return "maconbsd", nil })
	err := (&Package{name: "rsync"}).apply()
	if err == nil || !strings.Contains(err.Error(), "unsupported package manager") {
		t.Fatalf("apply err = %v, want unsupported package manager", err)
	}
}

// TestRunOrErr pins the three outcomes of runOrErr: start errors are
// wrapped, non-zero exits carry stdout/stderr, and success returns nil. The
// runner is passed in, so no package-level seam is patched.
func TestRunOrErr(t *testing.T) {
	tests := []struct {
		name    string
		run     runner
		wantErr string
	}{
		{
			name: "success",
			run:  func(string, ...string) (string, string, int, error) { return "", "", 0, nil },
		},
		{
			name:    "start error is wrapped",
			run:     func(string, ...string) (string, string, int, error) { return "", "", -1, errors.New("exec: gone") },
			wantErr: "pkg [install rsync]: exec: gone",
		},
		{
			name:    "non-zero exit carries stdout and stderr",
			run:     func(string, ...string) (string, string, int, error) { return "out\n", "err\n", 7, nil },
			wantErr: "failed (exit 7): out\nerr\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runOrErr(tt.run, "pkg", "install", "rsync")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("runOrErr err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("runOrErr err = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestPresentEnsureAbsentFake exercises the public constructors against the
// swapped detector and runner: Present registers and installs, Absent
// registers with IsAbsent, Ensure applies latest without registering.
func TestPresentEnsureAbsentFake(t *testing.T) {
	oldRun, oldDry := runCmd, resource.DryRun()
	defer func() {
		ResetDetectPackageManagerForTest()
		runCmd = oldRun
		resource.SetDryRun(oldDry)
	}()
	SetDetectPackageManagerForTest(func() (string, error) { return "freebsd", nil })

	// Present: not installed → pkg install runs and notes changed.
	resource.ResetRepository()
	var calls []pkgCall
	runCmd = fakePkgRunner(false, false, false, &calls)
	Present("rsync")
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertPkgAction(t, pkgCall{bin: "pkg", args: []string{"info", "-e", "rsync"}},
		[]string{"pkg", "install", "-y", "rsync"}, calls)
	assertPkgNote(t, "rsync", resource.StatusChanged)

	// Absent: not installed → converges ok without an action.
	resource.ResetRepository()
	calls = nil
	runCmd = fakePkgRunner(false, false, false, &calls)
	Absent("rsync")
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertPkgAction(t, pkgCall{bin: "pkg", args: []string{"info", "-e", "rsync"}}, nil, calls)
	assertPkgNote(t, "rsync", resource.StatusOK)

	// Ensure with IsLatest: applies without registering; the package is
	// installed so the latest path still acts (upgrade).
	calls = nil
	runCmd = fakePkgRunner(true, false, false, &calls)
	if err := Ensure("rsync", opt.IsLatest); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	assertPkgAction(t, pkgCall{bin: "pkg", args: []string{"info", "-e", "rsync"}},
		[]string{"pkg", "upgrade", "-y", "rsync"}, calls)
	assertPkgNote(t, "rsync", resource.StatusChanged)
}
