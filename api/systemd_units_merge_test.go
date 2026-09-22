package api

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
)

// twoUnitSources writes a.service and b.service sources into a temp dir and
// returns it.
func twoUnitSources(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixtureFile(t, filepath.Join(dir, "a.service"), "[Unit]\nDescription=a\n")
	writeFixtureFile(t, filepath.Join(dir, "b.service"), "[Unit]\nDescription=b\n")
	return dir
}

// opsByKind returns the recorded ops of kind k in recorded order.
func opsByKind(ops []plan.Op, k plan.Kind) []plan.Op {
	var out []plan.Op
	for _, op := range ops {
		if op.Op == k {
			out = append(out, op)
		}
	}
	return out
}

// assertMergedReload checks the shape every same-bus merge must record: one
// reload op watching and depending on both inputs, and each composition's
// activation depending on that reload while watching only its own input.
func assertMergedReload(t *testing.T, ops []plan.Op, reloadID string, user bool, aID, bID string) {
	t.Helper()
	reloads := opsByKind(ops, plan.KindDaemonReload)
	if len(reloads) != 1 {
		t.Fatalf("recorded %d daemon_reload ops, want exactly one: %#v", len(reloads), reloads)
	}
	r := reloads[0]
	both := []string{aID, bID}
	if r.ID != reloadID || r.User != user || !r.IfChanged {
		t.Fatalf("reload = %#v, want armed %s (user=%v)", r, reloadID, user)
	}
	if !reflect.DeepEqual(r.Watch, both) || !reflect.DeepEqual(r.Deps, both) {
		t.Fatalf("reload watch=%v deps=%v, want union %v", r.Watch, r.Deps, both)
	}
	timers := opsByKind(ops, plan.KindTimer)
	if len(timers) != 2 {
		t.Fatalf("timers = %#v, want one per composition", timers)
	}
	for i, own := range []string{aID, bID} {
		tm := timers[i]
		if !reflect.DeepEqual(tm.Watch, []string{own}) {
			t.Fatalf("timer %s watch = %v, want only its own input %s", tm.Name, tm.Watch, own)
		}
		if !reflect.DeepEqual(tm.Deps, []string{reloadID, own}) {
			t.Fatalf("timer %s deps = %v, want [%s %s]", tm.Name, tm.Deps, reloadID, own)
		}
		if tm.User != user {
			t.Fatalf("timer %s user = %v, want %v", tm.Name, tm.User, user)
		}
	}
}

func TestSystemdUnitsTwoSystemCompositionsMergeReload(t *testing.T) {
	dir := twoUnitSources(t)
	ops := systemdUnitsFixture(t, func() {
		a := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"))
		SystemdUnits(FanIn(a), ActivateTimer("a", options.WithRestart))
		b := InstallFile("/etc/systemd/system/b.service", filepath.Join(dir, "b.service"))
		SystemdUnits(FanIn(b), ActivateTimer("b", options.WithRestart))
	})
	assertMergedReload(t, ops, "DaemonReload[system]", false,
		"File[/etc/systemd/system/a.service]", "File[/etc/systemd/system/b.service]")
	// The reload keeps its first recorded position; apply orders it after
	// the later input through its deps (see systemd_units_merge_apply_test.go).
	if ops[2].Op != plan.KindDaemonReload {
		t.Fatalf("reload moved from its recorded position: %#v", ops)
	}
}

func TestSystemdUnitsTwoUserCompositionsMergeReload(t *testing.T) {
	dir := twoUnitSources(t)
	home := t.TempDir()
	aPath := filepath.Join(home, "a.service")
	bPath := filepath.Join(home, "b.service")
	ops := systemdUnitsFixture(t, func() {
		a := InstallFile(aPath, filepath.Join(dir, "a.service"))
		SystemdUnits(WithUserBus(), FanIn(a), ActivateTimer("a"))
		b := InstallFile(bPath, filepath.Join(dir, "b.service"))
		SystemdUnits(WithUserBus(), FanIn(b), ActivateTimer("b"))
	})
	assertMergedReload(t, ops, "DaemonReload[user]", true, "File["+aPath+"]", "File["+bPath+"]")
}

// TestSystemdUnitsMergesExplicitDaemonReload covers a composition next to an
// explicit DaemonReload on the same bus, in both orders. An OnChange reload
// merges into one gated reload; an unconditional reload keeps the merged
// reload unconditional, because "always" must not turn into "on change".
func TestSystemdUnitsMergesExplicitDaemonReload(t *testing.T) {
	dir := twoUnitSources(t)
	aID := "File[/etc/systemd/system/a.service]"
	bID := "File[/etc/systemd/system/b.service]"

	t.Run("composition-then-onchange", func(t *testing.T) {
		ops := systemdUnitsFixture(t, func() {
			a := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"))
			units := SystemdUnits(FanIn(a), ActivateService("a"))
			b := InstallFile("/etc/systemd/system/b.service", filepath.Join(dir, "b.service"))
			reload := DaemonReload(options.OnChange(b))
			if reload.Dependencies()[0] != units.Dependencies()[0] {
				t.Errorf("DaemonReload returned %v, want the composed reload %v", reload.Dependencies(), units.Dependencies())
			}
		})
		reloads := opsByKind(ops, plan.KindDaemonReload)
		if len(reloads) != 1 || !reloads[0].IfChanged ||
			!reflect.DeepEqual(reloads[0].Watch, []string{aID, bID}) ||
			!reflect.DeepEqual(reloads[0].Deps, []string{aID, bID}) {
			t.Fatalf("reloads = %#v, want one armed reload watching %v", reloads, []string{aID, bID})
		}
	})

	t.Run("unconditional-then-composition", func(t *testing.T) {
		ops := systemdUnitsFixture(t, func() {
			b := InstallFile("/etc/systemd/system/b.service", filepath.Join(dir, "b.service"))
			DaemonReload(options.DependsOn(b))
			a := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"))
			SystemdUnits(FanIn(a), ActivateService("a", options.WithRestart))
		})
		reloads := opsByKind(ops, plan.KindDaemonReload)
		if len(reloads) != 1 || reloads[0].IfChanged {
			t.Fatalf("reloads = %#v, want one unconditional reload", reloads)
		}
		if !reflect.DeepEqual(reloads[0].Deps, []string{aID, bID}) {
			t.Fatalf("reload deps = %v, want %v", reloads[0].Deps, []string{aID, bID})
		}
		// The activation keeps its own gate even though the reload is
		// unconditional.
		svc := opsByKind(ops, plan.KindService)[0]
		if !svc.IfChanged || !reflect.DeepEqual(svc.Watch, []string{aID}) {
			t.Fatalf("service gate = %#v, want armed on %s", svc, aID)
		}
	})
}

// TestSystemdUnitsOnePerBusKeepsSeparateReloads is the no-merge case: a
// system and a user composition keep one reload each.
func TestSystemdUnitsOnePerBusKeepsSeparateReloads(t *testing.T) {
	dir := twoUnitSources(t)
	userPath := filepath.Join(t.TempDir(), "b.service")
	ops := systemdUnitsFixture(t, func() {
		a := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"))
		SystemdUnits(FanIn(a), ActivateTimer("a"))
		b := InstallFile(userPath, filepath.Join(dir, "b.service"))
		SystemdUnits(WithUserBus(), FanIn(b), ActivateTimer("b"))
	})
	reloads := opsByKind(ops, plan.KindDaemonReload)
	if len(reloads) != 2 {
		t.Fatalf("reloads = %#v, want one per bus", reloads)
	}
	want := map[string][]string{
		"DaemonReload[system]": {"File[/etc/systemd/system/a.service]"},
		"DaemonReload[user]":   {"File[" + userPath + "]"},
	}
	for _, r := range reloads {
		if !reflect.DeepEqual(r.Watch, want[r.ID]) || !reflect.DeepEqual(r.Deps, want[r.ID]) {
			t.Fatalf("reload %s watch=%v deps=%v, want %v", r.ID, r.Watch, r.Deps, want[r.ID])
		}
	}
}

// singleCompositionBody is the exact JSONL body main (before same-bus
// merging) recorded for one composition; the merge must not change a byte
// of it. The header line carries plan.CurrentVersion, which other tasks bump.
const singleCompositionBody = `{"op":"file","id":"File[/etc/systemd/system/a.service]","path":"/etc/systemd/system/a.service","mode":"0644","content_b64":"W1VuaXRdCkRlc2NyaXB0aW9uPWEK","has_content":true}
{"op":"daemon_reload","id":"DaemonReload[system]","if_changed":true,"watch":["File[/etc/systemd/system/a.service]"],"deps":["File[/etc/systemd/system/a.service]"]}
{"op":"timer","id":"Timer[a.timer]","name":"a.timer","restart":true,"if_changed":true,"watch":["File[/etc/systemd/system/a.service]"],"deps":["DaemonReload[system]","File[/etc/systemd/system/a.service]"]}
`

func TestSystemdUnitsSingleCompositionPlanUnchanged(t *testing.T) {
	dir := twoUnitSources(t)
	ops := systemdUnitsFixture(t, func() {
		a := InstallFile("/etc/systemd/system/a.service", filepath.Join(dir, "a.service"), options.WithMode(0o644))
		SystemdUnits(FanIn(a), ActivateTimer("a", options.WithRestart))
	})
	got, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"op":"plan","version":%d,"id":"units"}`, plan.VersionSensitive-1) + "\n" + singleCompositionBody
	if string(got) != want {
		t.Fatalf("single-composition plan changed:\n got: %s\nwant: %s", got, want)
	}
}
