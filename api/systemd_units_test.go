package api

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
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
	ops, err := RecordPlan("units", t.TempDir(), "demo_units")
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
		if !reflect.DeepEqual(op.Deps, wantDeps) {
			t.Fatalf("service %d deps = %v, want %v", i, op.Deps, wantDeps)
		}
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
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	RegisterMethods(systemdUnitsTasks{body: func() {
		unit := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"), options.WithMode(0o644))
		composed = SystemdUnits(
			FanIn(unit),
			ActivateService("a-marker"),
		)
	}}, WithPrefix("demo_"))

	// Recording runs the task body, which builds the composition.
	if _, err := RecordPlan("units", "", "demo_units"); err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	if composed == nil {
		t.Fatal("SystemdUnits returned nil")
	}
	deps := composed.Dependencies()
	want := []string{"DaemonReload[system]", "Service[a-marker]"}
	if !reflect.DeepEqual(deps, want) {
		t.Fatalf("composition dependencies = %v, want %v (inputs stay caller-owned)", deps, want)
	}
}
