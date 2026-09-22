package systemdtimer

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

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

// TestJoinedTimerRefusesMergeOfRelatedInput (bb2): a timer whose service
// wants or is after b.service joins the reload while b.service is no input
// yet; the later declaration that makes the File installing b.service an
// input of the (already joined) reload is refused fail-fast, instead of
// recording a reload that loads b.service only after the timer started.
// logger.Fatal exits, so each case runs in a helper process.
func TestJoinedTimerRefusesMergeOfRelatedInput(t *testing.T) {
	for _, name := range []string{"wants", "after"} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestJoinedTimerMergeFatalHelperProcess$", "-test.timeout=60s")
			cmd.Env = append(os.Environ(), "GONF_SYSTEMDTIMER_JOIN_MERGE_FATAL="+name)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("case %s exited 0; output:\n%s", name, out)
			}
			for _, want := range []string{
				"DaemonReload[system]: cannot merge",
				"SystemdTimer[wall]",
				"references b.service",
			} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("case %s output misses %q:\n%s", name, want, out)
				}
			}
		})
	}
}

// TestJoinedTimerMergeFatalHelperProcess is the helper process for
// TestJoinedTimerRefusesMergeOfRelatedInput; it must never exit 0.
func TestJoinedTimerMergeFatalHelperProcess(t *testing.T) {
	name := os.Getenv("GONF_SYSTEMDTIMER_JOIN_MERGE_FATAL")
	if name == "" {
		return
	}
	resource.ResetRepository()
	related := opt.WithWants("b.service")
	if name == "after" {
		related = opt.WithAfter("b.service")
	}
	declareJoinThenMerge(related)
	t.Fatalf("case %q merged without refusing the joined timer", name)
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
