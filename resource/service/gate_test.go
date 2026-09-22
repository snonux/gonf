package service

import (
	"github.com/snonux/gonf/internal/testutil"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// gateCase is one row of TestChangeGateOutcomes: the watched id the armed
// gate consults, the status noted for File[w] before the service applies
// (zero means nothing was noted), and what the shared policy must do.
type gateCase struct {
	name      string
	watch     string
	watchNote resource.Status
	noted     bool
	dryRun    bool
	enabled   bool
	wantDone  []verb
	wantDesc  []verb
	wantCount string // the PrintSummary counts line, watched note included
	noRestart bool   // neither WithRestart nor WithReload
}

// gateCases pins the change gate's observable outcomes on a running
// WithRestart service: the gate holds (unchanged or unknown watched id), a
// watched change fires the restart, a would-change fires it under dry-run
// without performing it, and convergence still runs while the restart is
// held.
func gateCases() []gateCase {
	return []gateCase{
		{name: "unchanged watch holds restart", watch: "File[w]", watchNote: resource.StatusOK, noted: true,
			enabled: true, wantCount: "1 ok, 0 changed, 1 skipped, 0 would-change"},
		{name: "unknown watch id holds restart", watch: "File[never-noted]", enabled: true,
			wantCount: "0 ok, 0 changed, 1 skipped, 0 would-change"},
		{name: "watched change fires restart", watch: "File[w]", watchNote: resource.StatusChanged, noted: true,
			enabled: true, wantDone: []verb{verbRestart}, wantDesc: []verb{verbRestart},
			wantCount: "0 ok, 2 changed, 0 skipped, 0 would-change"},
		{name: "dry-run would-change fires described restart only", watch: "File[w]",
			watchNote: resource.StatusWouldChange, noted: true, dryRun: true, enabled: true,
			wantDesc: []verb{verbRestart}, wantCount: "0 ok, 0 changed, 0 skipped, 2 would-change"},
		{name: "dry-run held restart is skipped", watch: "File[w]", watchNote: resource.StatusOK, noted: true,
			dryRun: true, enabled: true, wantCount: "1 ok, 0 changed, 1 skipped, 0 would-change"},
		// Armed gate but no restart/reload requested: nothing is gated, so
		// the converged service is ok, not skipped.
		{name: "gated running service without restart is ok", watch: "File[w]", watchNote: resource.StatusOK,
			noted: true, enabled: true, noRestart: true, wantCount: "2 ok, 0 changed, 0 skipped, 0 would-change"},
		{name: "held restart still converges enable", watch: "File[w]", watchNote: resource.StatusOK, noted: true,
			wantDone: []verb{verbEnable}, wantDesc: []verb{verbEnable},
			wantCount: "1 ok, 1 changed, 0 skipped, 0 would-change"},
	}
}

// TestChangeGateOutcomes characterizes the service change gate end to end
// through applyWith and a fake backend: which verbs run, which are only
// described, and what the apply summary reports.
func TestChangeGateOutcomes(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })

	for _, tt := range gateCases() {
		t.Run(tt.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tt.dryRun)
			if tt.noted {
				resource.Note("File[w]", tt.watchNote)
			}
			svc := withRestartSvc(Service{name: "d"})
			if tt.noRestart {
				svc = Service{name: "d"}
			}
			svc.SetChangeWatch([]string{tt.watch})
			fb := &fakeBackend{isRunning: true, isEnabled: tt.enabled}

			if err := svc.applyWith(fb); err != nil {
				t.Fatalf("applyWith: %v", err)
			}
			if !slices.Equal(fb.done, tt.wantDone) {
				t.Errorf("done = %v, want %v", fb.done, tt.wantDone)
			}
			if !slices.Equal(fb.describedFor, tt.wantDesc) {
				t.Errorf("described = %v, want %v", fb.describedFor, tt.wantDesc)
			}
			var buf strings.Builder
			resource.PrintSummary(&buf)
			if !strings.Contains(buf.String(), "summary: "+tt.wantCount) {
				t.Errorf("summary = %q, want counts %q", buf.String(), tt.wantCount)
			}
		})
	}
}

// TestPlanDraftChangeGate pins the draft wiring of the gate: an armed gate
// records IfChanged plus a copy of the watched ids (later mutation of the
// resource must not leak into the recorded draft); an unarmed gate records
// neither.
func TestPlanDraftChangeGate(t *testing.T) {
	var ungated Service
	ungated.name = "d"
	if d := ungated.planDraft("Service[d]"); d.IfChanged || d.Watch != nil {
		t.Errorf("ungated draft IfChanged=%t Watch=%#v, want false/nil", d.IfChanged, d.Watch)
	}

	gated := Service{name: "d"}
	gated.SetChangeWatch([]string{"File[a]", "File[b]"})
	d := gated.planDraft("Service[d]")
	if !d.IfChanged || !reflect.DeepEqual(d.Watch, []string{"File[a]", "File[b]"}) {
		t.Fatalf("gated draft IfChanged=%t Watch=%#v", d.IfChanged, d.Watch)
	}
	gated.Watch[0] = "File[mutated]"
	if d.Watch[0] != "File[a]" {
		t.Errorf("draft Watch aliases the resource's slice: %#v", d.Watch)
	}
}

// TestHeldGateLogLine pins the operator-visible debug line for a held
// restart/reload, word for word (the wording lives in
// embed.ChangeGate.LogHeld; Service names its action "restart/reload").
func TestHeldGateLogLine(t *testing.T) {
	resource.ResetReport()
	output := testutil.CaptureLog(t, logger.LevelDebug)

	svc := withRestartSvc(Service{name: "d"})
	svc.SetChangeWatch([]string{"File[never-noted]"})
	if err := svc.applyWith(&fakeBackend{isRunning: true, isEnabled: true}); err != nil {
		t.Fatalf("applyWith: %v", err)
	}
	want := "Service[d]: restart/reload held by change gate (no watched dependency changed)\n"
	if got := output(); got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}
