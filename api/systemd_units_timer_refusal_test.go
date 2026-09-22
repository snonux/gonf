package api

import (
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// assertNotJoined checks that the one recorded reload depends only on the
// composition's input, i.e. the SystemdTimer did not join it.
func assertNotJoined(t *testing.T, ops []plan.Op, input string) {
	t.Helper()
	reloads := opsByKind(ops, plan.KindDaemonReload)
	if len(reloads) != 1 || !reflect.DeepEqual(reloads[0].Deps, []string{input}) {
		t.Fatalf("reloads = %#v, want one depending only on %s (join refused)", reloads, input)
	}
}

// TestSystemdTimerJoinRefusedForRelatedUnit: a timer whose service
// Wants=/After= a unit the composition installs must not be converged
// before the composition's reload, so it keeps its own reload after it
// (two reloads when both changed, as before u82); an unrelated
// Wants= still joins.
func TestSystemdTimerJoinRefusedForRelatedUnit(t *testing.T) {
	f := newTimerJoinFixture(t)
	in := "File[" + filepath.Join(f.dst, "a.service") + "]"
	for _, opt := range []options.SystemdTimerOption{options.WithWants("a.service"), options.WithAfter("a.service")} {
		assertNotJoined(t, systemdUnitsFixture(t, func() { f.units(true); f.timer(opt) }), in)
	}
	ops := systemdUnitsFixture(t, func() { f.units(true); f.timer(options.WithWants("network-online.target")) })
	if r := opsByKind(ops, plan.KindDaemonReload); len(r) != 1 || !slices.Contains(r[0].Deps, "SystemdTimer[wall]") {
		t.Fatalf("unrelated Wants= did not join: %#v", r)
	}

	f.apply(t, func() { f.units(true); f.timer(options.WithWants("a.service")) })
	if user, _ := f.reloads(); user != 2 {
		t.Fatalf("refused join reloaded %d times, want 2 (composition, then timer): %v", user, f.invoked)
	}
}

// TestSystemdTimerJoinRefusedAtWhenBoundary: a when-block between the
// recorded reload and the timer (either side) refuses the join instead of
// recording a dependency the destination could not order; the recorded plan
// passes the controller pre-flight and applies (the destination's
// dependency sort accepts it) with the timer keeping its own reload.
func TestSystemdTimerJoinRefusedAtWhenBoundary(t *testing.T) {
	for name, body := range map[string]func(f *timerJoinFixture){
		"timer in a later when-block": func(f *timerJoinFixture) {
			f.units(true)
			WhenPathExists(f.src, func() { f.timer() })
		},
		"composition in an earlier when-block": func(f *timerJoinFixture) {
			WhenPathExists(f.src, func() { f.units(true) })
			f.timer()
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTimerJoinFixture(t)
			in := "File[" + filepath.Join(f.dst, "a.service") + "]"
			assertNotJoined(t, systemdUnitsFixture(t, func() { body(f) }), in)
			f.apply(t, func() { body(f) })
			if user, _ := f.reloads(); user != 2 {
				t.Fatalf("reloads = %d, want 2 (composition, then timer): %v", user, f.invoked)
			}
		})
	}
}

// TestSystemdTimerJoinRefusedAtPrivilegeBoundary: a reload recorded by a
// Privileged nested Run is not joined by the unprivileged caller's timer,
// nor an unprivileged reload by a timer of a Privileged nested Run. The
// plan passes the controller pre-flight (ValidateChunks) and every chunk
// dry-run applies through the destination's dependency sort.
func TestSystemdTimerJoinRefusedAtPrivilegeBoundary(t *testing.T) {
	if runtime.GOOS != "linux" || !systemd.Detected() {
		t.Skip("systemd dry-run apply is Linux-specific")
	}
	src := twoUnitSources(t)
	aPath := "/etc/systemd/system/a.service"
	units := func() {
		a := InstallFile(aPath, filepath.Join(src, "a.service"))
		SystemdUnits(FanIn(a), ActivateTimer("a"))
	}
	timer := func() {
		SystemdTimer("wall", options.WithCommand("/bin/true"), options.WithOnCalendar("hourly"))
	}
	for name, bodies := range map[string][2]func(){
		"privileged reload, unprivileged timer": {func() { _ = Run("probe_inner"); timer() }, units},
		"unprivileged reload, privileged timer": {func() { units(); _ = Run("probe_inner") }, timer},
	} {
		t.Run(name, func(t *testing.T) {
			ops := recordTwoTasks(t, bodies[0], bodies[1])
			assertNotJoined(t, ops, "File["+aPath+"]")
			chunks := plan.SplitPrivilegeChunks(ops)
			if len(chunks) < 2 {
				t.Fatalf("recorded %d privilege chunks, want the boundary: %#v", len(chunks), ops)
			}
			if err := plan.ValidateChunks(chunks); err != nil {
				t.Fatalf("ValidateChunks: %v", err)
			}
			dryRunChunks(t, chunks)
		})
	}
}

// dryRunChunks applies every chunk in dry-run mode against a systemctl
// stub, so the destination's dependency sort runs without mutating the
// host.
func dryRunChunks(t *testing.T, chunks []plan.Chunk) {
	t.Helper()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	testseam.FakeSystemctl(t, func(string, ...string) (string, string, int, error) { return "", "", 0, nil })
	for i, ch := range chunks {
		if err := plan.Apply(ch.Ops, plan.Facts{GOOS: runtime.GOOS}, ""); err != nil {
			t.Fatalf("dry-run apply of chunk %d: %v", i, err)
		}
	}
}
