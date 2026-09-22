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
	return resource.Register("File", path, resource.ApplierFunc(func() error { return nil }))
}

// declareJoinThenMerge is the bb2 sequence at resource level (what
// SystemdUnits(FanIn(a)); SystemdTimer("wall", ...);
// SystemdUnits(FanIn(b)) registers): a same-bus reload watching a.service,
// then the joining timer with extra options, then a second reload
// declaration watching b.service, merged into the first. It returns the
// reload.
func declareJoinThenMerge(timerOpts ...opt.SystemdTimerOption) resource.Resource {
	a := unitFile("/etc/systemd/system/a.service")
	reload := systemd.Present(opt.OnChange(a))
	Present("wall", append([]opt.SystemdTimerOption{opt.WithCommand("/bin/true"), opt.WithOnCalendar("hourly")}, timerOpts...)...)
	b := unitFile("/etc/systemd/system/b.service")
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
