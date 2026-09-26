package pkg

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// applyVia returns an apply function that converges a Package through the
// given backend with the Package's own runner (the WithEnv-aware seam), the
// same wiring Package.apply uses after backend selection.
func applyVia(b backend) func(*Package) error {
	return func(p *Package) error { return p.applyWith(b, p.run) }
}

// fakeBackend is an in-memory backend: it reports a fixed probe result,
// hands out recognisable commands, and records what the policy asked it to
// execute. It runs nothing, so it proves the policy depends only on the
// backend interface and not on any OS tool or package-level seam.
type fakeBackend struct {
	isInstalled bool
	probeErr    error
	execErr     error
	upgradeSaw  []bool     // installed argument of every upgradeCmd call
	executed    []*command // commands handed to execute
}

func (f *fakeBackend) installed(runner, string) (bool, error) {
	return f.isInstalled, f.probeErr
}

func (f *fakeBackend) installCmd(name string) command {
	return command{bin: "fake", label: "fake", args: []string{"install", name}}
}

func (f *fakeBackend) upgradeCmd(name string, installed bool) command {
	f.upgradeSaw = append(f.upgradeSaw, installed)
	return command{bin: "fake", label: "fake", args: []string{"upgrade", name}}
}

func (f *fakeBackend) removeCmd(name string) command {
	return command{bin: "fake", label: "fake", args: []string{"remove", name}}
}

func (f *fakeBackend) execute(_ runner, c command) error {
	f.executed = append(f.executed, &c)
	return f.execErr
}

// panicRunner fails the test if anything reaches it: the fake backend must
// never need a runner.
func panicRunner(t *testing.T) runner {
	return func(bin string, args ...string) (string, string, int, error) {
		t.Fatalf("runner reached with %s %v; the fake backend must not run commands", bin, args)
		return "", "", -1, nil
	}
}

// TestApplyWithFakeBackendPolicy pins the shared package policy against a
// fake backend injected directly into applyWith — no detector or runner
// global is patched: which transition runs for each desired/probed state,
// that IsLatest always acts and passes the probe to upgradeCmd, that Absent
// wins over IsLatest, and that dry-run executes nothing.
func TestApplyWithFakeBackendPolicy(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })

	tests := []struct {
		name        string
		pkg         Package
		installed   bool
		dryRun      bool
		wantArgs    []string // executed command args; nil = nothing executed
		wantUpgrade []bool   // installed values upgradeCmd saw
		wantNote    resource.Status
	}{
		{name: "present and installed converges", pkg: Package{name: "rsync"}, installed: true, wantNote: resource.StatusOK},
		{name: "present and missing installs", pkg: Package{name: "rsync"}, wantArgs: []string{"install", "rsync"}, wantNote: resource.StatusChanged},
		{name: "absent and missing converges", pkg: absentPkg("rsync"), wantNote: resource.StatusOK},
		{name: "absent and installed removes", pkg: absentPkg("rsync"), installed: true, wantArgs: []string{"remove", "rsync"}, wantNote: resource.StatusChanged},
		{name: "latest and installed upgrades", pkg: Package{name: "rsync", latest: true}, installed: true,
			wantArgs: []string{"upgrade", "rsync"}, wantUpgrade: []bool{true}, wantNote: resource.StatusChanged},
		{name: "latest and missing asks upgrade with installed=false", pkg: Package{name: "rsync", latest: true},
			wantArgs: []string{"upgrade", "rsync"}, wantUpgrade: []bool{false}, wantNote: resource.StatusChanged},
		{name: "absent wins over latest", pkg: func() Package { p := absentPkg("rsync"); p.latest = true; return p }(), installed: true,
			wantArgs: []string{"remove", "rsync"}, wantNote: resource.StatusChanged},
		{name: "dry-run executes nothing", pkg: Package{name: "rsync"}, dryRun: true, wantNote: resource.StatusWouldChange},
		{name: "dry-run converged stays ok", pkg: Package{name: "rsync"}, installed: true, dryRun: true, wantNote: resource.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)
			fb := &fakeBackend{isInstalled: tt.installed}

			if err := tt.pkg.applyWith(fb, panicRunner(t)); err != nil {
				t.Fatalf("applyWith: %v", err)
			}
			assertExecuted(t, fb, tt.wantArgs)
			if !slices.Equal(fb.upgradeSaw, tt.wantUpgrade) {
				t.Errorf("upgradeCmd saw installed=%v, want %v", fb.upgradeSaw, tt.wantUpgrade)
			}
			assertPkgNote(t, tt.pkg.name, tt.wantNote)
		})
	}
}

// TestApplyWithFakeBackendErrors pins the negative paths: a probe error
// surfaces before any command is chosen, and an execute error surfaces
// without the resource being noted as changed.
func TestApplyWithFakeBackendErrors(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	resource.SetDryRun(false)

	t.Run("probe error", func(t *testing.T) {
		resource.ResetReport()
		fb := &fakeBackend{probeErr: errors.New("probe boom")}
		err := (&Package{name: "rsync"}).applyWith(fb, panicRunner(t))
		if err == nil || !strings.Contains(err.Error(), "probe boom") {
			t.Fatalf("err = %v, want probe boom", err)
		}
		assertExecuted(t, fb, nil)
		assertNotNoted(t, "rsync")
	})

	t.Run("execute error", func(t *testing.T) {
		resource.ResetReport()
		fb := &fakeBackend{execErr: errors.New("exec boom")}
		err := (&Package{name: "rsync"}).applyWith(fb, panicRunner(t))
		if err == nil || !strings.Contains(err.Error(), "exec boom") {
			t.Fatalf("err = %v, want exec boom", err)
		}
		assertExecuted(t, fb, []string{"install", "rsync"})
		assertNotNoted(t, "rsync")
	})
}

// TestBackendsRunThroughInjectedRunner shows a real backend needs no global
// seam either: the runner handed to applyWith receives the probe and the
// action, while the Package's own runners (injected as traps) fail the test
// if reached.
func TestBackendsRunThroughInjectedRunner(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	resource.SetDryRun(false)
	trap := func(bin string, args ...string) (string, string, int, error) {
		t.Fatalf("package-level runner reached with %s %v", bin, args)
		return "", "", -1, nil
	}
	var calls []pkgCall
	p := &Package{
		name:     "rsync",
		runFn:    trap,
		runEnvFn: func(_ []string, bin string, args ...string) (string, string, int, error) { return trap(bin, args...) },
	}
	if err := p.applyWith(openbsdBackend{}, fakePkgRunner(false, false, false, &calls)); err != nil {
		t.Fatalf("applyWith: %v", err)
	}
	assertPkgAction(t, pkgCall{bin: "pkg_info", args: []string{"-e", "rsync-*"}}, []string{"pkg_add", "rsync"}, calls)
}

// TestBackendCommands pins every backend's command vectors, including the
// OpenBSD upgrade that falls back to a plain install for a missing package.
func TestBackendCommands(t *testing.T) {
	tests := []struct {
		name string
		got  command
		want []string // bin followed by args
	}{
		{"dnf install", dnfBackend{}.installCmd("x"), []string{"dnf", "install", "-y", "x"}},
		// dnf update and pkg upgrade cannot install, so a missing package
		// is installed instead (like pkg_add below).
		{"dnf upgrade installed", dnfBackend{}.upgradeCmd("x", true), []string{"dnf", "update", "-y", "x"}},
		{"dnf upgrade missing", dnfBackend{}.upgradeCmd("x", false), []string{"dnf", "install", "-y", "x"}},
		{"dnf remove", dnfBackend{}.removeCmd("x"), []string{"dnf", "remove", "-y", "x"}},
		{"freebsd install", freebsdBackend{}.installCmd("x"), []string{"pkg", "install", "-y", "x"}},
		{"freebsd upgrade installed", freebsdBackend{}.upgradeCmd("x", true), []string{"pkg", "upgrade", "-y", "x"}},
		{"freebsd upgrade missing", freebsdBackend{}.upgradeCmd("x", false), []string{"pkg", "install", "-y", "x"}},
		{"freebsd remove", freebsdBackend{}.removeCmd("x"), []string{"pkg", "remove", "-y", "x"}},
		{"netbsd install", netbsdBackend{}.installCmd("x"), []string{netbsdPkgin, "-y", "install", "x"}},
		{"netbsd upgrade", netbsdBackend{}.upgradeCmd("x", true), []string{netbsdPkgin, "-y", "install", "x"}},
		{"netbsd remove", netbsdBackend{}.removeCmd("x"), []string{netbsdPkgin, "-y", "remove", "x"}},
		{"openbsd install", openbsdBackend{}.installCmd("x"), []string{"pkg_add", "x"}},
		{"openbsd upgrade installed", openbsdBackend{}.upgradeCmd("x", true), []string{"pkg_add", "-u", "x"}},
		{"openbsd upgrade missing", openbsdBackend{}.upgradeCmd("x", false), []string{"pkg_add", "x"}},
		{"openbsd remove", openbsdBackend{}.removeCmd("x"), []string{"pkg_delete", "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := append([]string{tt.got.bin}, tt.got.args...)
			if !slices.Equal(got, tt.want) {
				t.Errorf("command = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBackendLogLines pins the operator-visible dry-run and apply log lines
// per backend, including the NetBSD label (pkgin, not its absolute path) and
// the dnf apply line with its historical " completed" suffix, so the backend
// refactor stays wording-neutral.
func TestBackendLogLines(t *testing.T) {
	tests := []struct {
		name      string
		c         command
		wantWould string
		wantDid   string
	}{
		{"dnf", dnfBackend{}.installCmd("x"), "dry-run: would run dnf [install -y x]", "dnf [install -y x] completed"},
		{"dnf remove", dnfBackend{}.removeCmd("x"), "dry-run: would run dnf [remove -y x]", "dnf [remove -y x] completed"},
		{"freebsd", freebsdBackend{}.upgradeCmd("x", true), "dry-run: would run pkg [upgrade -y x]", "pkg [upgrade -y x]"},
		{"netbsd", netbsdBackend{}.removeCmd("x"), "dry-run: would run pkgin [-y remove x]", "pkgin [-y remove x]"},
		{"openbsd add", openbsdBackend{}.upgradeCmd("x", true), "dry-run: would run pkg_add [-u x]", "pkg_add [-u x]"},
		{"openbsd delete", openbsdBackend{}.removeCmd("x"), "dry-run: would run pkg_delete [x]", "pkg_delete [x]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			would, did := tt.c.logLines()
			if would != tt.wantWould {
				t.Errorf("dry-run line = %q, want %q", would, tt.wantWould)
			}
			if did != tt.wantDid {
				t.Errorf("apply line = %q, want %q", did, tt.wantDid)
			}
		})
	}
}

// TestSelectBackend pins the name-to-backend table: every detector name
// selects its own backend type, and an unknown name or a detector error is
// refused before anything runs.
func TestSelectBackend(t *testing.T) {
	want := map[string]backend{
		"dnf": dnfBackend{}, "freebsd": freebsdBackend{}, "netbsd": netbsdBackend{}, "openbsd": openbsdBackend{},
	}
	if len(backends) != len(want) {
		t.Fatalf("backends table has %d entries, want %d", len(backends), len(want))
	}
	for name, wantB := range want {
		got, err := managedBy(func() (string, error) { return name, nil }).selectBackend()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if reflect.TypeOf(got) != reflect.TypeOf(wantB) {
			t.Errorf("%s selected %T, want %T", name, got, wantB)
		}
	}

	if _, err := managedBy(func() (string, error) { return "brew", nil }).selectBackend(); err == nil || !strings.Contains(err.Error(), "unsupported package manager") {
		t.Errorf("unknown manager err = %v, want unsupported package manager", err)
	}
	if _, err := managedBy(func() (string, error) { return "", errors.New("detect boom") }).selectBackend(); err == nil || !strings.Contains(err.Error(), "detect boom") {
		t.Errorf("detector err = %v, want detect boom", err)
	}
}

// managedBy returns a Package whose package-manager detection is injected
// as detect, as buildWith would from runners.PackageRunners.Manager.
func managedBy(detect func() (string, error)) *Package {
	return &Package{name: "rsync", managerFn: detect}
}

// ranBy returns a copy of p whose plain package-manager runner is injected
// as run, as buildWith would from runners.PackageRunners.Run.
func ranBy(p Package, run runner) *Package {
	p.runFn = run
	return &p
}

func absentPkg(name string) Package {
	p := Package{name: name}
	p.Absent = true
	return p
}

func assertExecuted(t *testing.T, fb *fakeBackend, wantArgs []string) {
	t.Helper()
	if wantArgs == nil {
		if len(fb.executed) != 0 {
			t.Fatalf("executed %v, want nothing", fb.executed[0].args)
		}
		return
	}
	if len(fb.executed) != 1 || !slices.Equal(fb.executed[0].args, wantArgs) {
		t.Fatalf("executed %d command(s), want exactly %v", len(fb.executed), wantArgs)
	}
}

// assertNotNoted checks that a failed apply recorded no status for the
// package (in particular not "changed").
func assertNotNoted(t *testing.T, name string) {
	t.Helper()
	var buf strings.Builder
	resource.PrintSummary(&buf)
	if strings.Contains(buf.String(), "Package["+name+"]") {
		t.Errorf("failed apply was noted:\n%s", buf.String())
	}
}
