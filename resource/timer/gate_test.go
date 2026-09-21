package timer

import (
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// timerGateCase is one row of TestChangeGateOutcomes: the watched id the
// armed gate consults, the status the watched resource notes (noted=false
// means the watched id never reports), and the expected systemctl mutations
// and summary counts (watched note included).
type timerGateCase struct {
	name      string
	watch     string
	watchNote resource.Status
	noted     bool
	dryRun    bool
	enabled   bool
	wantVerbs []string
	wantCount string
	noRestart bool // register without WithRestart
}

// timerGateCases pins the timer change gate: an unchanged or unknown watch
// holds the restart (skipped), a watched change fires it, dry-run never
// mutates, and convergence (enable) still runs while the restart is held.
func timerGateCases() []timerGateCase {
	return []timerGateCase{
		{name: "unchanged watch holds restart", watch: "File[unit]", watchNote: resource.StatusOK, noted: true,
			enabled: true, wantCount: "1 ok, 0 changed, 1 skipped, 0 would-change"},
		{name: "unknown watch id holds restart", watch: "File[never-noted]", enabled: true,
			wantCount: "0 ok, 0 changed, 1 skipped, 0 would-change"},
		{name: "watched change fires restart", watch: "File[unit]", watchNote: resource.StatusChanged, noted: true,
			enabled: true, wantVerbs: []string{"restart"}, wantCount: "0 ok, 2 changed, 0 skipped, 0 would-change"},
		{name: "dry-run would-change does not mutate", watch: "File[unit]", watchNote: resource.StatusWouldChange,
			noted: true, dryRun: true, enabled: true, wantCount: "0 ok, 0 changed, 0 skipped, 2 would-change"},
		// Armed gate but no WithRestart: nothing is gated, so the converged
		// timer is ok, not skipped (the restart check precedes the gate).
		{name: "gated active timer without restart is ok", watch: "File[unit]", watchNote: resource.StatusOK,
			noted: true, enabled: true, noRestart: true, wantCount: "2 ok, 0 changed, 0 skipped, 0 would-change"},
		{name: "held restart still converges enable", watch: "File[unit]", watchNote: resource.StatusOK, noted: true,
			wantVerbs: []string{"enable"}, wantCount: "1 ok, 1 changed, 0 skipped, 0 would-change"},
	}
}

// TestChangeGateOutcomes characterizes the timer change gate through the
// registered apply path with a fake systemctl.
func TestChangeGateOutcomes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	t.Cleanup(systemd.ResetRunCmdForTest)

	for _, tc := range timerGateCases() {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			resource.SetDryRun(tc.dryRun)
			verbs := fakeTimerSystemctl(tc.enabled)
			// A noted watch goes through OnChange so the watched resource is
			// also ordered first; the unknown id uses the ids-level form.
			gate := opt.WatchChanges(tc.watch)
			if tc.noted {
				gate = opt.OnChange(resource.Register("File", "unit", resource.ApplierFunc(func() error {
					resource.Note("File[unit]", tc.watchNote)
					return nil
				})))
			}
			opts := []opt.TimerOption{gate}
			if !tc.noRestart {
				opts = append(opts, opt.WithRestart)
			}
			Present("fstrim", opts...)
			if err := resource.Apply(); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !slices.Equal(*verbs, tc.wantVerbs) {
				t.Errorf("systemctl mutations = %v, want %v", *verbs, tc.wantVerbs)
			}
			var buf strings.Builder
			resource.PrintSummary(&buf)
			if !strings.Contains(buf.String(), "summary: "+tc.wantCount) {
				t.Errorf("summary = %q, want counts %q", buf.String(), tc.wantCount)
			}
		})
	}
}

// fakeTimerSystemctl installs a systemctl fake that reports the timer as
// active (and enabled when enabled is true) and records every mutating verb.
func fakeTimerSystemctl(enabled bool) *[]string {
	verbs := &[]string{}
	systemd.SetRunCmdForTest(func(name string, args ...string) (string, string, int, error) {
		switch {
		case contains(args, "is-active"):
			return "", "", 0, nil
		case contains(args, "is-enabled"):
			if enabled {
				return "", "", 0, nil
			}
			return "", "", 1, nil
		}
		for _, v := range []string{"enable", "start", "restart", "stop", "disable"} {
			if contains(args, v) {
				*verbs = append(*verbs, v)
			}
		}
		return "", "", 0, nil
	})
	return verbs
}

// TestPlanDraftChangeGate pins the timer draft wiring: an armed gate
// records IfChanged plus an unaliased copy of the watched ids; an unarmed
// one records neither.
func TestPlanDraftChangeGate(t *testing.T) {
	ungated := Timer{name: "a.timer"}
	if d := ungated.planDraft("Timer[a.timer]"); d.IfChanged || d.Watch != nil {
		t.Errorf("ungated draft IfChanged=%t Watch=%#v, want false/nil", d.IfChanged, d.Watch)
	}

	gated := Timer{name: "a.timer"}
	gated.SetChangeWatch([]string{"File[a]"})
	d := gated.planDraft("Timer[a.timer]")
	if !d.IfChanged || !reflect.DeepEqual(d.Watch, []string{"File[a]"}) {
		t.Fatalf("gated draft IfChanged=%t Watch=%#v", d.IfChanged, d.Watch)
	}
	gated.Watch[0] = "File[mutated]"
	if d.Watch[0] != "File[a]" {
		t.Errorf("draft Watch aliases the resource's slice: %#v", d.Watch)
	}
}

// TestHeldGateLogLine pins the operator-visible debug line for a held timer
// restart, word for word (the wording lives in embed.ChangeGate.LogHeld;
// Timer names its action "restart").
func TestHeldGateLogLine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	t.Cleanup(systemd.ResetRunCmdForTest)
	fakeTimerSystemctl(true)
	output, restore := logger.CaptureForTest(logger.LevelDebug)
	defer restore()

	Present("fstrim", opt.WithRestart, opt.WatchChanges("File[never-noted]"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "Timer[fstrim.timer]: restart held by change gate (no watched dependency changed)"
	if !slices.Contains(strings.Split(output(), "\n"), want) {
		t.Errorf("log %q lacks the line %q", output(), want)
	}
}
