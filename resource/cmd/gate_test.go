package cmd

import (
	"reflect"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/resource"
)

// cmdGateCase is one row of TestChangeGateOutcomes: the watched id, the
// status the watched resource notes (noted=false: never reports), whether
// the command runner must be invoked, and the summary counts (watched note
// included).
type cmdGateCase struct {
	name      string
	watch     string
	watchNote resource.Status
	noted     bool
	dryRun    bool
	wantRun   bool
	wantCount string
}

// TestChangeGateOutcomes characterizes the command change gate: the gate
// holds (skipped) for an unchanged or unknown watched id, a watched change
// runs the command, and under dry-run a would-change fires the gate but
// Mutate still keeps the runner from being invoked.
func TestChangeGateOutcomes(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })

	for _, tc := range []cmdGateCase{
		{name: "unchanged watch skips", watch: "File[unit]", watchNote: resource.StatusOK, noted: true,
			wantCount: "1 ok, 0 changed, 1 skipped, 0 would-change"},
		{name: "unknown watch id skips", watch: "File[never-noted]",
			wantCount: "0 ok, 0 changed, 1 skipped, 0 would-change"},
		{name: "watched change runs", watch: "File[unit]", watchNote: resource.StatusChanged, noted: true,
			wantRun: true, wantCount: "0 ok, 2 changed, 0 skipped, 0 would-change"},
		{name: "dry-run would-change fires without running", watch: "File[unit]",
			watchNote: resource.StatusWouldChange, noted: true, dryRun: true,
			wantCount: "0 ok, 0 changed, 0 skipped, 2 would-change"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			resource.SetDryRun(tc.dryRun)
			ran := false
			testseam.FakeCommand(t, testseam.Command{Run: func(exec.Opts, string, ...string) (string, string, int, error) {
				ran = true
				return "", "", 0, nil
			}})
			// A noted watch goes through OnChange so the watched resource is
			// also ordered first and the plan engine applies both. The
			// unknown id uses the ids-level form and the direct Ensure path:
			// the plan pre-flight refuses a dangling watch before apply, so
			// the gate's own "never noted holds" rule is only reachable there.
			if !tc.noted {
				resource.ResetReport()
				if err := Ensure("true", nil, opt.WatchChanges(tc.watch)); err != nil {
					t.Fatalf("Ensure: %v", err)
				}
			} else {
				Present("true", nil, opt.OnChange(testapply.Register("File", "unit", testapply.Noting(tc.watchNote, "File[unit]"))))
				if err := testapply.Apply(); err != nil {
					t.Fatalf("Apply: %v", err)
				}
			}
			if ran != tc.wantRun {
				t.Errorf("runner invoked = %t, want %t", ran, tc.wantRun)
			}
			var buf strings.Builder
			resource.PrintSummary(&buf)
			if !strings.Contains(buf.String(), "summary: "+tc.wantCount) {
				t.Errorf("summary = %q, want counts %q", buf.String(), tc.wantCount)
			}
		})
	}
}

// TestPlanDraftChangeGate pins the command draft wiring: an armed gate
// records IfChanged plus an unaliased copy of the watched ids; an unarmed
// one records neither.
func TestPlanDraftChangeGate(t *testing.T) {
	ungated := Cmd{name: "x", bin: "true"}
	if d := ungated.planDraft("Command[x]"); d.IfChanged || d.Watch != nil {
		t.Errorf("ungated draft IfChanged=%t Watch=%#v, want false/nil", d.IfChanged, d.Watch)
	}

	gated := Cmd{name: "x", bin: "true"}
	gated.SetChangeWatch([]string{"File[a]"})
	d := gated.planDraft("Command[x]")
	if !d.IfChanged || !reflect.DeepEqual(d.Watch, []string{"File[a]"}) {
		t.Fatalf("gated draft IfChanged=%t Watch=%#v", d.IfChanged, d.Watch)
	}
	gated.Watch[0] = "File[mutated]"
	if d.Watch[0] != "File[a]" {
		t.Errorf("draft Watch aliases the resource's slice: %#v", d.Watch)
	}
}
