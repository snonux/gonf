package api

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// systemdUnitsFixture registers one task whose body composes SystemdUnits and
// returns the recorded plan ops for it.
func systemdUnitsFixture(t *testing.T, body func()) []plan.Op {
	t.Helper()
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	RegisterMethods(systemdUnitsTasks{body: body}, WithPrefix("demo_"))
	ops, err := RecordPlan("units", testutil.PrivateTempDir(t), "demo_units")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	return ops
}

type systemdUnitsTasks struct {
	body func()
}

func (systemdUnitsTasks) DescUnits() string { return "composed systemd units" }
func (t systemdUnitsTasks) Units()          { t.body() }

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSystemdUnitsComposesOneReloadAndFanIn(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "a.service"), "[Unit]\n")
	writeFixtureFile(t, filepath.Join(dir, "drop.conf"), "[Service]\n")
	writeFixtureFile(t, filepath.Join(dir, "check.sh"), "run\n")

	ops := systemdUnitsFixture(t, func() {
		unit := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"), options.WithMode(0o644))
		dropIn := InstallFile("/etc/systemd/system/b.service.d/10-x.conf", filepath.Join(dir, "drop.conf"))
		script := InstallFile("/usr/local/bin/a-check", filepath.Join(dir, "check.sh"), options.WithMode(0o755))
		SystemdUnits(
			FanIn(unit, dropIn, script),
			ActivateTimer("a-run", options.WithRestart),
			ActivateServices(List("a-marker", "a-drain")),
		)
	})

	// plan + 3 file ops + one reload + timer + 2 services.
	if len(ops) != 8 {
		t.Fatalf("ops = %d (%#v), want 8", len(ops), ops)
	}
	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindFile, plan.KindFile, plan.KindFile,
		plan.KindDaemonReload, plan.KindTimer, plan.KindService, plan.KindService,
	}
	gotKinds := make([]plan.Kind, 0, len(ops))
	for _, op := range ops {
		gotKinds = append(gotKinds, op.Op)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("op kinds = %v, want %v", gotKinds, wantKinds)
	}

	inputIDs := []string{
		"File[/etc/systemd/system/a.service]",
		"File[/etc/systemd/system/b.service.d/10-x.conf]",
		"File[/usr/local/bin/a-check]",
	}

	reload := ops[4]
	if reload.ID != "DaemonReload[system]" {
		t.Fatalf("reload id = %q", reload.ID)
	}
	if reload.User {
		t.Fatalf("reload unexpectedly on user bus")
	}
	if !reload.IfChanged {
		t.Fatalf("reload not change-gated: %#v", reload)
	}
	if !reflect.DeepEqual(reload.Watch, inputIDs) {
		t.Fatalf("reload watch = %v, want %v", reload.Watch, inputIDs)
	}
	if !reflect.DeepEqual(reload.Deps, inputIDs) {
		t.Fatalf("reload deps = %v, want %v", reload.Deps, inputIDs)
	}

	timerOp := ops[5]
	if timerOp.Name != "a-run.timer" || !timerOp.Restart || timerOp.User {
		t.Fatalf("timer op = %#v", timerOp)
	}
	if !timerOp.IfChanged || !reflect.DeepEqual(timerOp.Watch, inputIDs) {
		t.Fatalf("timer gate watch = %#v, want %v", timerOp, inputIDs)
	}
	wantDeps := append([]string{"DaemonReload[system]"}, inputIDs...)
	if !reflect.DeepEqual(timerOp.Deps, wantDeps) {
		t.Fatalf("timer deps = %v, want %v", timerOp.Deps, wantDeps)
	}

	for i, op := range ops[6:8] {
		if op.Name != []string{"a-marker", "a-drain"}[i] {
			t.Fatalf("service %d name = %q", i, op.Name)
		}
		if op.Restart || op.User {
			t.Fatalf("service %d unexpectedly restarts or uses the user bus: %#v", i, op)
		}
		// The uniform activation gate is inert without a restart/reload
		// policy, but it must still be armed and watching the inputs — a
		// future WithRestart on this activation relies on it.
		if !op.IfChanged || !reflect.DeepEqual(op.Watch, inputIDs) {
			t.Fatalf("service %d gate = %#v, want armed and watching %v", i, op, inputIDs)
		}
		if !reflect.DeepEqual(op.Deps, wantDeps) {
			t.Fatalf("service %d deps = %v, want %v", i, op.Deps, wantDeps)
		}
	}
}

func TestSystemdUnitsFanInDuplicateInputsDeduplicateWatch(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "a.service"), "[Unit]\n")

	ops := systemdUnitsFixture(t, func() {
		unit := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"), options.WithMode(0o644))
		SystemdUnits(
			FanIn(unit, unit),
			ActivateTimer("a-run", options.WithRestart),
		)
	})

	// plan + file + reload + timer.
	if len(ops) != 4 {
		t.Fatalf("ops = %d (%#v), want 4", len(ops), ops)
	}
	unitID := "File[/etc/systemd/system/a.service]"
	if !reflect.DeepEqual(ops[2].Watch, []string{unitID}) {
		t.Fatalf("reload watch = %v, want the input id exactly once", ops[2].Watch)
	}
	if !reflect.DeepEqual(ops[2].Deps, []string{unitID}) {
		t.Fatalf("reload deps = %v, want %v", ops[2].Deps, []string{unitID})
	}
	if !reflect.DeepEqual(ops[3].Watch, []string{unitID}) {
		t.Fatalf("timer watch = %v, want the input id exactly once", ops[3].Watch)
	}
	wantDeps := []string{"DaemonReload[system]", unitID}
	if !reflect.DeepEqual(ops[3].Deps, wantDeps) {
		t.Fatalf("timer deps = %v, want %v", ops[3].Deps, wantDeps)
	}
}

func TestSystemdUnitsUserBusAndMultiFanIn(t *testing.T) {
	dir := t.TempDir()
	unitsSrc := filepath.Join(dir, "src")
	writeFixtureFile(t, filepath.Join(unitsSrc, "x.service"), "[Unit]\n")
	writeFixtureFile(t, filepath.Join(unitsSrc, "x.timer"), "[Timer]\n")
	writeFixtureFile(t, filepath.Join(dir, "drain"), "run\n")

	ops := systemdUnitsFixture(t, func() {
		units := SyncDir(filepath.Join(dir, "user"), unitsSrc+"/*")
		drain := InstallFile(filepath.Join(dir, "bin", "drain"), filepath.Join(dir, "drain"))
		SystemdUnits(
			WithUserBus(),
			FanIn(units, drain),
			ActivateTimers(List("x", "y")),
		)
	})

	// plan + sync_dir + file + reload + 2 timers.
	if len(ops) != 6 {
		t.Fatalf("ops = %d (%#v), want 6", len(ops), ops)
	}

	syncID := "Directory[" + filepath.Join(dir, "user") + "]"
	drainID := "File[" + filepath.Join(dir, "bin", "drain") + "]"
	watch := []string{syncID, drainID}

	reload := ops[3]
	if reload.ID != "DaemonReload[user]" || !reload.User || !reload.IfChanged {
		t.Fatalf("reload op = %#v", reload)
	}
	if !reflect.DeepEqual(reload.Watch, watch) {
		t.Fatalf("reload watch = %v, want %v (SyncDir Multi must flatten)", reload.Watch, watch)
	}

	wantDeps := []string{"DaemonReload[user]", syncID, drainID}
	for i, op := range ops[4:6] {
		if op.Op != plan.KindTimer {
			t.Fatalf("op %d = %s, want timer", i+4, op.Op)
		}
		if op.Name != []string{"x.timer", "y.timer"}[i] {
			t.Fatalf("timer %d name = %q", i, op.Name)
		}
		if op.Restart {
			t.Fatalf("timer %d restarts without WithRestart: %#v", i, op)
		}
		if !op.User || !op.IfChanged || !reflect.DeepEqual(op.Watch, watch) {
			t.Fatalf("timer %d = %#v, want user bus, change gate on %v", i, op, watch)
		}
		if !reflect.DeepEqual(op.Deps, wantDeps) {
			t.Fatalf("timer %d deps = %v, want %v", i, op.Deps, wantDeps)
		}
	}
}

func TestSystemdUnitsActivationsShareOptions(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "a.service"), "[Unit]\n")

	ops := systemdUnitsFixture(t, func() {
		unit := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"), options.WithMode(0o644))
		SystemdUnits(
			FanIn(unit),
			ActivateTimer("a-run", options.WithRestart),
			ActivateService("a-marker", options.WithReload),
		)
	})

	// plan + file + reload + timer + service.
	if len(ops) != 5 {
		t.Fatalf("ops = %d (%#v), want 5", len(ops), ops)
	}
	timerOp := ops[3]
	if timerOp.Op != plan.KindTimer || !timerOp.Restart || !timerOp.IfChanged {
		t.Fatalf("timer op = %#v", timerOp)
	}
	svcOp := ops[4]
	if svcOp.Op != plan.KindService || !svcOp.Reload || !svcOp.IfChanged {
		t.Fatalf("service op = %#v", svcOp)
	}
}

func TestSystemdUnitsUserBusCoversServices(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "a.service"), "[Unit]\n")

	ops := systemdUnitsFixture(t, func() {
		unit := InstallFile(filepath.Join(dir, "units", "a.service"), filepath.Join(dir, "a.service"), options.WithMode(0o644))
		SystemdUnits(
			WithUserBus(),
			FanIn(unit),
			ActivateService("a-marker"),
		)
	})

	// plan + file + reload + service.
	if len(ops) != 4 {
		t.Fatalf("ops = %d (%#v), want 4", len(ops), ops)
	}
	if ops[2].ID != "DaemonReload[user]" || !ops[2].User {
		t.Fatalf("reload op = %#v", ops[2])
	}
	svcOp := ops[3]
	if svcOp.Op != plan.KindService || !svcOp.User {
		t.Fatalf("service op = %#v, want user bus", svcOp)
	}
	if !reflect.DeepEqual(svcOp.Deps, []string{"DaemonReload[user]", "File[" + filepath.Join(dir, "units", "a.service") + "]"}) {
		t.Fatalf("service deps = %v", svcOp.Deps)
	}
}

func TestSystemdUnitsReturnsComposedMembers(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "a.service"), "[Unit]\n")

	var composed Resource
	systemdUnitsFixture(t, func() {
		unit := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"), options.WithMode(0o644))
		composed = SystemdUnits(
			FanIn(unit),
			ActivateService("a-marker"),
		)
	})

	if composed == nil {
		t.Fatal("SystemdUnits returned nil")
	}
	deps := composed.Dependencies()
	want := []string{"DaemonReload[system]", "Service[a-marker]"}
	if !reflect.DeepEqual(deps, want) {
		t.Fatalf("composition dependencies = %v, want %v (inputs stay caller-owned)", deps, want)
	}
}

// TestSystemdUnitsRecordedPlanApplies round-trips a composed plan through the
// real destination apply path with a stubbed systemctl: the unit files land,
// exactly one daemon-reload fires, and the restart-on-change fan-in reaches
// the timer.
func TestSystemdUnitsRecordedPlanApplies(t *testing.T) {
	if runtime.GOOS != "linux" || !systemd.Detected() {
		t.Skip("systemd apply fake is Linux-specific")
	}
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "a.service"), "[Unit]\n")
	writeFixtureFile(t, filepath.Join(dir, "check.sh"), "run\n")
	// Real recipes ensure the destination dirs; mirror that so the file
	// apply can stage its temp files.
	for _, sub := range []string{"units", "bin"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	RegisterMethods(systemdUnitsTasks{body: func() {
		unit := InstallFile(filepath.Join(dir, "units", "a.service"), filepath.Join(dir, "a.service"), options.WithMode(0o644))
		script := InstallFile(filepath.Join(dir, "bin", "check"), filepath.Join(dir, "check.sh"), options.WithMode(0o755))
		SystemdUnits(
			FanIn(unit, script),
			ActivateTimer("a-run", options.WithRestart),
			ActivateService("a-marker"),
		)
	}}, WithPrefix("demo_"))

	ops, err := RecordPlan("apply-units", testutil.PrivateTempDir(t), "demo_units")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	var invoked [][]string
	testseam.FakeSystemctl(t, func(name string, args ...string) (string, string, int, error) {
		if name == "systemctl" {
			invoked = append(invoked, args)
		}
		// Report every queried unit as active and enabled so the only
		// possible mutating action is the gated timer restart.
		return "", "", 0, nil
	})

	if err := plan.Apply(ops, plan.Facts{GOOS: runtime.GOOS}, ""); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}

	for _, dst := range []string{
		filepath.Join(dir, "units", "a.service"),
		filepath.Join(dir, "bin", "check"),
	} {
		if _, err := os.Stat(dst); err != nil {
			t.Fatalf("apply did not install %s: %v", dst, err)
		}
	}

	reloads := 0
	restarted := false
	for _, args := range invoked {
		switch {
		case args[0] == "daemon-reload":
			reloads++
		case args[0] == "restart" && len(args) == 2 && args[1] == "a-run.timer":
			restarted = true
		case args[0] == "enable" || args[0] == "start":
			t.Fatalf("unexpected systemctl %v with units already active and enabled", args)
		}
	}
	if reloads != 1 {
		t.Fatalf("daemon-reload ran %d times, want exactly once: %v", reloads, invoked)
	}
	if !restarted {
		t.Fatalf("changed inputs did not fan into the timer restart: %v", invoked)
	}

	// A second apply of unchanged inputs must hold the gate: no reload, no
	// restart, no enable/start.
	invoked = nil
	if err := plan.Apply(ops, plan.Facts{GOOS: runtime.GOOS}, ""); err != nil {
		t.Fatalf("second plan.Apply: %v", err)
	}
	for _, args := range invoked {
		switch args[0] {
		case "daemon-reload", "restart", "enable", "start", "stop", "disable":
			t.Fatalf("unchanged second apply must not mutate, got systemctl %v", args)
		}
	}
}

// TestSystemdUnitsRegistrationMisuseIsDeclarationError checks the
// composition's registration-time misuse in-process: it is reported as a
// declaration error with the specific message, the checks run in a fixed
// order (inputs first, whatever order the options came in), nothing of the
// composition is registered, and an empty Multi comes back.
func TestSystemdUnitsRegistrationMisuseIsDeclarationError(t *testing.T) {
	unitFile := func(t *testing.T) Resource {
		dir := t.TempDir()
		writeFixtureFile(t, filepath.Join(dir, "a.service"), "[Unit]\n")
		return InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"), options.WithMode(0o644))
	}
	cases := []struct {
		caseName, want string
		compose        func(t *testing.T) Resource
	}{
		{"no-inputs", "no watchable managed inputs", func(*testing.T) Resource { return SystemdUnits() }},
		{"no-inputs-and-empty-name", "no watchable managed inputs", func(*testing.T) Resource {
			return SystemdUnits(ActivateTimer(""), ActivateService(""))
		}},
		{"empty-timer-name", "ActivateTimer name must not be empty", func(t *testing.T) Resource {
			return SystemdUnits(FanIn(unitFile(t)), ActivateService("a.service"), ActivateTimer(""))
		}},
		{"empty-service-name", "ActivateService name must not be empty", func(t *testing.T) Resource {
			return SystemdUnits(FanIn(unitFile(t)), ActivateService(""))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.caseName, func(t *testing.T) {
			var got Resource
			requireDeclErr(t, tc.want, func() { got = tc.compose(t) })
			if deps := got.Dependencies(); len(deps) != 0 {
				t.Fatalf("refused composition returned %v, want an empty Multi", deps)
			}
			for _, id := range resource.RegisteredIDs() {
				if !strings.HasPrefix(id, "File[") {
					t.Fatalf("refused composition registered %s (all: %v)", id, resource.RegisteredIDs())
				}
			}
		})
	}
}
