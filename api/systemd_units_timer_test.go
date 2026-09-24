package api

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource/systemd"
)

// timerJoinFixture drives a task combining SystemdUnits and SystemdTimer
// (u82): src holds the composition's unit source, dst its installed copy,
// home the HOME a user SystemdTimer writes its units under, and invoked
// every stubbed systemctl call of the last apply.
type timerJoinFixture struct {
	src, dst, home string
	command        string // the SystemdTimer's ExecStart, changed to change its units
	// inputFirst makes the stub fail a reload that runs before the
	// composition's input is installed (off where the order is legitimate).
	inputFirst bool
	invoked    [][]string
}

// newTimerJoinFixture prepares the sources and a temporary HOME. Recording
// needs no systemd; apply does and skips off systemd hosts.
func newTimerJoinFixture(t *testing.T) *timerJoinFixture {
	t.Helper()
	f := &timerJoinFixture{src: t.TempDir(), dst: t.TempDir(), home: t.TempDir(), command: "/bin/true", inputFirst: true}
	t.Setenv("HOME", f.home)
	writeFixtureFile(t, filepath.Join(f.src, "a.service"), "[Unit]\nDescription=a\n")
	return f
}

// units declares the composition (on the user bus when user) and returns it.
func (f *timerJoinFixture) units(user bool) Resource {
	in := InstallFile(filepath.Join(f.dst, "a.service"), filepath.Join(f.src, "a.service"))
	opts := []SystemdUnitsOption{FanIn(in), ActivateTimer("a", options.WithRestart)}
	if user {
		opts = append(opts, WithUserBus())
	}
	return SystemdUnits(opts...)
}

// timer declares the user-bus SystemdTimer "wall" with extra options.
func (f *timerJoinFixture) timer(extra ...options.SystemdTimerOption) Resource {
	opts := append([]options.SystemdTimerOption{options.WithUser, options.WithRestart,
		options.WithCommand(f.command), options.WithOnCalendar("hourly")}, extra...)
	return SystemdTimer("wall", opts...)
}

// combined is the dotfiles home_systemd_user shape: the user composition,
// then the user SystemdTimer.
func (f *timerJoinFixture) combined() {
	f.units(true)
	f.timer()
}

// apply records body and applies the plan against the systemctl stub,
// which reports every unit active and enabled and checks that each reload
// runs only after the composition's input is installed.
func (f *timerJoinFixture) apply(t *testing.T, body func()) []plan.Op {
	t.Helper()
	if runtime.GOOS != "linux" || !systemd.Detected() {
		t.Skip("systemd apply fake is Linux-specific")
	}
	ops := systemdUnitsFixture(t, body)
	f.invoked = nil
	sysR := &runners.SystemdRunners{Run: func(name string, args ...string) (string, string, int, error) {
		if name != "systemctl" {
			return "", "", 0, nil
		}
		f.invoked = append(f.invoked, args)
		if f.inputFirst && slices.Contains(args, "daemon-reload") {
			if _, err := os.Stat(filepath.Join(f.dst, "a.service")); err != nil {
				t.Errorf("daemon-reload ran before the composition's input was installed")
			}
		}
		return "", "", 0, nil
	}}
	ctx := runners.WithSet(context.Background(), &runners.Set{Systemd: sysR})
	if err := plan.ApplyWithContext(ctx, ops, plan.Facts{GOOS: runtime.GOOS}, ""); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}
	return ops
}

// reloads counts the daemon-reloads of the last apply on the user bus and
// on the system bus.
func (f *timerJoinFixture) reloads() (user, system int) {
	for _, args := range f.invoked {
		switch {
		case reflect.DeepEqual(args, []string{"--user", "daemon-reload"}):
			user++
		case reflect.DeepEqual(args, []string{"daemon-reload"}):
			system++
		}
	}
	return user, system
}

// firstIndex returns the position of the first invocation matching pred, or -1.
func (f *timerJoinFixture) firstIndex(pred func([]string) bool) int {
	return slices.IndexFunc(f.invoked, pred)
}

// TestSystemdTimerJoinsSameBusReloadRecord pins the recorded shape of the
// combined task: still exactly one daemon_reload op, ordered after the
// SystemdTimer by one extra dep, and a systemd_timer op identical to the
// one a standalone SystemdTimer records.
func TestSystemdTimerJoinsSameBusReloadRecord(t *testing.T) {
	f := newTimerJoinFixture(t)
	standalone := opsByKind(systemdUnitsFixture(t, func() { f.timer() }), plan.KindSystemdTimer)
	ops := systemdUnitsFixture(t, f.combined)

	reloads := opsByKind(ops, plan.KindDaemonReload)
	if len(reloads) != 1 {
		t.Fatalf("recorded %d daemon_reload ops, want 1: %#v", len(reloads), reloads)
	}
	in := "File[" + filepath.Join(f.dst, "a.service") + "]"
	r := reloads[0]
	if r.ID != "DaemonReload[user]" || !r.User || !r.IfChanged ||
		!reflect.DeepEqual(r.Watch, []string{in}) || !reflect.DeepEqual(r.Deps, []string{in, "SystemdTimer[wall]"}) {
		t.Fatalf("reload = %#v, want the gated user reload watching %s, deps [%s SystemdTimer[wall]]", r, in, in)
	}
	if got := opsByKind(ops, plan.KindSystemdTimer); !reflect.DeepEqual(got, standalone) {
		t.Fatalf("joined systemd_timer op %#v differs from the standalone one %#v", got, standalone)
	}
}

// TestSystemdTimerAloneRecordsNoReloadOp: a standalone SystemdTimer records
// only its own op, with no deps and no daemon_reload op.
func TestSystemdTimerAloneRecordsNoReloadOp(t *testing.T) {
	f := newTimerJoinFixture(t)
	ops := systemdUnitsFixture(t, func() { f.timer() })
	if n := len(opsByKind(ops, plan.KindDaemonReload)); n != 0 {
		t.Fatalf("standalone SystemdTimer recorded %d daemon_reload ops", n)
	}
	st := opsByKind(ops, plan.KindSystemdTimer)
	if len(st) != 1 || len(st[0].Deps) != 0 {
		t.Fatalf("systemd_timer ops = %#v, want one without deps", st)
	}
}

// TestSystemdTimerDoesNotJoinOtherBusOrCycle covers the refused joins: the
// system composition's reload is not ordered after a user timer, nor is a
// reload the timer already depends on (that edge would close a cycle; the
// timer then keeps reloading after it, without failing the recipe).
func TestSystemdTimerDoesNotJoinOtherBusOrCycle(t *testing.T) {
	f := newTimerJoinFixture(t)
	in := "File[" + filepath.Join(f.dst, "a.service") + "]"
	for name, body := range map[string]func(){
		"other bus": func() { f.units(false); f.timer() },
		"cycle":     func() { f.timer(options.DependsOn(f.units(true))) },
	} {
		t.Run(name, func(t *testing.T) {
			reloads := opsByKind(systemdUnitsFixture(t, body), plan.KindDaemonReload)
			if len(reloads) != 1 || !reflect.DeepEqual(reloads[0].Deps, []string{in}) {
				t.Fatalf("reloads = %#v, want one depending only on %s", reloads, in)
			}
		})
	}
}

// TestSystemdTimerJoinedReloadAppliesOnce is the apply-time contract of the
// combined task: whichever part changed, the user bus reloads exactly once,
// after the composition's input is installed and before any restart, and
// not at all when nothing changed.
func TestSystemdTimerJoinedReloadAppliesOnce(t *testing.T) {
	f := newTimerJoinFixture(t)
	isReload := func(a []string) bool { return slices.Contains(a, "daemon-reload") }
	isRestart := func(a []string) bool { return slices.Contains(a, "restart") }

	f.apply(t, f.combined) // everything new
	if user, _ := f.reloads(); user != 1 {
		t.Fatalf("first apply reloaded the user bus %d times, want 1: %v", user, f.invoked)
	}
	if r, s := f.firstIndex(isReload), f.firstIndex(isRestart); s >= 0 && s < r {
		t.Fatalf("a restart ran before the reload: %v", f.invoked)
	}

	f.apply(t, f.combined) // nothing changed
	if user, _ := f.reloads(); user != 0 {
		t.Fatalf("unchanged apply reloaded %d times: %v", user, f.invoked)
	}

	writeFixtureFile(t, filepath.Join(f.src, "a.service"), "[Unit]\nDescription=a2\n")
	f.apply(t, f.combined) // only the composition's input changed
	if user, _ := f.reloads(); user != 1 {
		t.Fatalf("changed composition input reloaded %d times, want 1: %v", user, f.invoked)
	}

	f.command = "/bin/false"
	f.apply(t, f.combined) // only the SystemdTimer's units changed
	if user, _ := f.reloads(); user != 1 {
		t.Fatalf("changed SystemdTimer units reloaded %d times, want 1: %v", user, f.invoked)
	}
}

// TestSystemdTimerApplyNegativeCases pins the cases the join leaves as they
// were before u82, each still correct: a timer declared before the
// composition applies before the composition's input is written, so its
// reload cannot cover that input and both reload; a timer on the other bus
// leaves one reload per bus; a timer that depends on the composition
// cannot join (cycle) and reloads for its own units after the
// composition's reload.
func TestSystemdTimerApplyNegativeCases(t *testing.T) {
	for _, tc := range []struct {
		name               string
		body               func(f *timerJoinFixture)
		inputFirst         bool
		wantUser, wantSyst int
	}{
		{name: "timer declared first", wantUser: 2,
			body: func(f *timerJoinFixture) { f.timer(); f.units(true) }},
		{name: "other bus", inputFirst: true, wantUser: 1, wantSyst: 1,
			body: func(f *timerJoinFixture) { f.units(false); f.timer() }},
		{name: "timer depends on the composition", inputFirst: true, wantUser: 2,
			body: func(f *timerJoinFixture) { f.timer(options.DependsOn(f.units(true))) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTimerJoinFixture(t)
			f.inputFirst = tc.inputFirst
			f.apply(t, func() { tc.body(f) })
			if user, syst := f.reloads(); user != tc.wantUser || syst != tc.wantSyst {
				t.Fatalf("reloads user=%d system=%d, want %d/%d: %v", user, syst, tc.wantUser, tc.wantSyst, f.invoked)
			}
		})
	}
}

// TestSystemdTimerOwnDropInReloadsBeforeRestart (4c2) is the apply-level
// regression test for the join.go related-unit gap: a FanIn composition
// installing a drop-in under the SystemdTimer's own <base>.service.d/ or
// <base>.timer.d/ must still reload the bus before the timer restarts, the
// same guarantee u82/bb2 give a composition whose input is an unrelated
// unit. Before 4c2 the timer's own unit names were missing from the
// related-unit set JoinRegisteredReload and the bb2 merge re-check use, so
// neither recognized the drop-in as belonging to the timer: the join went
// through, and on a later apply that only changed the drop-in, the timer
// restarted with the old drop-in still loaded before the reload that would
// have picked it up.
func TestSystemdTimerOwnDropInReloadsBeforeRestart(t *testing.T) {
	for _, tc := range []struct{ name, dropInDir string }{
		{"service drop-in", "wall.service.d"},
		{"timer drop-in", "wall.timer.d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS != "linux" || !systemd.Detected() {
				t.Skip("systemd apply fake is Linux-specific")
			}
			src, dst, home := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			dropInDst := filepath.Join(dst, tc.dropInDir, "10.conf")
			if err := os.MkdirAll(filepath.Dir(dropInDst), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFixtureFile(t, filepath.Join(src, "10.conf"), "[Service]\nEnvironment=X=1\n")

			body := func() {
				d := InstallFile(dropInDst, filepath.Join(src, "10.conf"))
				SystemdUnits(FanIn(d), WithUserBus())
				SystemdTimer("wall", options.WithUser, options.WithRestart,
					options.WithCommand("/bin/true"), options.WithOnCalendar("hourly"))
			}

			var invoked [][]string
			apply := func() {
				ops := systemdUnitsFixture(t, body)
				invoked = nil
				sysR := &runners.SystemdRunners{Run: func(name string, args ...string) (string, string, int, error) {
					if name != "systemctl" {
						return "", "", 0, nil
					}
					invoked = append(invoked, args)
					return "", "", 0, nil
				}}
				ctx := runners.WithSet(context.Background(), &runners.Set{Systemd: sysR})
				if err := plan.ApplyWithContext(ctx, ops, plan.Facts{GOOS: runtime.GOOS}, ""); err != nil {
					t.Fatalf("plan.Apply: %v", err)
				}
			}
			apply() // everything new

			writeFixtureFile(t, filepath.Join(src, "10.conf"), "[Service]\nEnvironment=X=2\n")
			apply() // only the drop-in changed

			isReload := func(a []string) bool { return slices.Contains(a, "daemon-reload") }
			isRestart := func(a []string) bool { return slices.Contains(a, "restart") }
			r, s := slices.IndexFunc(invoked, isReload), slices.IndexFunc(invoked, isRestart)
			if r < 0 || s < 0 {
				t.Fatalf("expected both a reload and a restart, got %v", invoked)
			}
			if s < r {
				t.Fatalf("restart ran before the reload that would pick up the changed drop-in: %v", invoked)
			}
		})
	}
}
