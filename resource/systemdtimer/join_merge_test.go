package systemdtimer

import (
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/systemd"
)

// unitFile registers a do-nothing File resource standing in for an
// installed unit file, and returns its ID.
func unitFile(path string) resource.Resource {
	return resource.Register("File", path, func() error { return nil })
}

// declareJoinThenMerge is the bb2 sequence at resource level (what
// SystemdUnits(FanIn(a)); SystemdTimer("wall", ...);
// SystemdUnits(FanIn(b)) registers): a same-bus reload watching a.service,
// then the joining timer with extra options, then a second reload
// declaration watching b.service, merged into the first. It returns the
// reload.
func declareJoinThenMerge(timerOpts ...opt.SystemdTimerOption) resource.Resource {
	return declareJoinThenMergeInput("/etc/systemd/system/b.service", timerOpts...)
}

// declareJoinThenMergeInput is declareJoinThenMerge with the later
// declaration's input path made a parameter, so a 4c2 case can merge a
// drop-in under the timer's own <base>.service.d/ or <base>.timer.d/
// instead of an unrelated b.service.
func declareJoinThenMergeInput(bPath string, timerOpts ...opt.SystemdTimerOption) resource.Resource {
	a := unitFile("/etc/systemd/system/a.service")
	reload := systemd.Present(opt.OnChange(a))
	Present("wall", append([]opt.SystemdTimerOption{opt.WithCommand("/bin/true"), opt.WithOnCalendar("hourly")}, timerOpts...)...)
	b := unitFile(bPath)
	systemd.Present(opt.OnChange(b))
	return reload
}

// TestJoinedTimerRefusesMergeOfRelatedInput (bb2, cb2): a timer whose
// service wants or is after b.service (alone or within a space-separated
// entry) joins the reload while b.service is no input yet; the later
// declaration that makes the File installing b.service an input of the
// (already joined) reload is refused with a declaration error
// (internal/declerr), instead of recording a reload that loads b.service only
// after the timer started.
func TestJoinedTimerRefusesMergeOfRelatedInput(t *testing.T) {
	for _, name := range []string{"wants", "after", "listed"} {
		t.Run(name, func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			related := opt.WithWants("b.service")
			switch name {
			case "after":
				related = opt.WithAfter("b.service")
			case "listed": // one entry naming two units, as systemd splits it (cb2)
				related = opt.WithWants("network-online.target b.service")
			}
			declareJoinThenMerge(related)
			err := declerr.First()
			if err == nil {
				t.Fatalf("case %q merged without refusing the joined timer", name)
			}
			for _, want := range []string{
				"DaemonReload[system]: cannot merge",
				"SystemdTimer[wall]",
				"references b.service",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("case %s error misses %q: %v", name, want, err)
				}
			}
		})
	}
}

// TestJoinedTimerKeepsMergeOfUnrelatedInput: a joined timer that
// references neither merged input does not block the merge; the reload
// watches both inputs and stays ordered after the timer.
func TestJoinedTimerKeepsMergeOfUnrelatedInput(t *testing.T) {
	for _, extra := range [][]opt.SystemdTimerOption{nil, {opt.WithWants("network-online.target"), opt.WithAfter("c.service")}} {
		resource.ResetRepository()
		t.Cleanup(resource.ResetRepository)
		reload := declareJoinThenMerge(extra...)
		var draft resource.PlanDraft
		for _, d := range resource.RegisteredPlanDrafts() {
			if d.ID == reload.ID() {
				draft = d
			}
		}
		if !slices.Contains(draft.Deps, "SystemdTimer[wall]") || len(draft.Watch) != 2 {
			t.Fatalf("extra %d: merged reload deps=%v watch=%v, want it after the timer watching both inputs", len(extra), draft.Deps, draft.Watch)
		}
	}
}

// joined reports whether reload's registered draft depends on
// SystemdTimer[wall], i.e. the timer joined it.
func joined(t *testing.T, reload resource.Resource) bool {
	t.Helper()
	for _, d := range resource.RegisteredPlanDrafts() {
		if d.ID == reload.ID() {
			return slices.Contains(d.Deps, "SystemdTimer[wall]")
		}
	}
	t.Fatalf("no registered draft for %s", reload.ID())
	return false
}

// TestTimerOwnUnitDropInBlocksJoin (4c2): before this, JoinRegisteredReload
// only checked the timer's After=/Wants= entries against the reload's
// inputs, so a same-bus reload whose only input is the timer's own unit
// file or a drop-in under <base>.service.d/ or <base>.timer.d/ was not
// recognized as related and the join went through. The timer's own units
// must always be in the related set: it is converged (including any
// restart) before the joined reload, so starting it from a stale drop-in
// is exactly the hazard u82/bb2 guard against for an unrelated unit.
func TestTimerOwnUnitDropInBlocksJoin(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"service unit file", "/etc/systemd/system/wall.service"},
		{"service drop-in", "/etc/systemd/system/wall.service.d/10.conf"},
		{"timer unit file", "/etc/systemd/system/wall.timer"},
		{"timer drop-in", "/etc/systemd/system/wall.timer.d/10.conf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			f := unitFile(tc.path)
			reload := systemd.Present(opt.OnChange(f))
			Present("wall", opt.WithCommand("/bin/true"), opt.WithOnCalendar("hourly"))
			if joined(t, reload) {
				t.Fatalf("timer joined the reload although %s may define its own unit", tc.path)
			}
		})
	}
}

// TestJoinedTimerRefusesMergeOfOwnUnitDropIn (4c2) is the merge-time half of
// TestTimerOwnUnitDropInBlocksJoin: the timer joins on an unrelated first
// input, then a later same-bus declaration's input is a drop-in under the
// timer's own <base>.service.d/ or <base>.timer.d/; the merge must be
// refused exactly as it is for a related After=/Wants= entry (bb2), since
// the joiner is already converged before the merged reload.
func TestJoinedTimerRefusesMergeOfOwnUnitDropIn(t *testing.T) {
	for _, tc := range []struct{ name, path, unit string }{
		{"service drop-in", "/etc/systemd/system/wall.service.d/10.conf", "wall.service"},
		{"timer drop-in", "/etc/systemd/system/wall.timer.d/10.conf", "wall.timer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			declareJoinThenMergeInput(tc.path)
			err := declerr.First()
			if err == nil {
				t.Fatalf("case %q merged without refusing the joined timer", tc.name)
			}
			for _, want := range []string{
				"DaemonReload[system]: cannot merge",
				"SystemdTimer[wall]",
				"references " + tc.unit,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("case %s error misses %q: %v", tc.name, want, err)
				}
			}
		})
	}
}
