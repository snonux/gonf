package systemd

import (
	"reflect"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// draftWatchCase is one TestDaemonReloadDraftWatch case: the options of a
// daemon-reload and the gate fields its plan draft must record.
type draftWatchCase struct {
	name      string
	opts      []opt.DaemonReloadOption
	wantGated bool
	wantWatch []string
}

// draftWatchCases are the daemon-reload draft shapes recorded plans depend
// on, all as recorded before b72: gate ids first, then the (last) WithWatch
// ids, else the DependsOn ids, recorded armed or not.
var draftWatchCases = []draftWatchCase{
	{name: "plain"},
	{name: "ungated deps still recorded as watch",
		opts:      []opt.DaemonReloadOption{opt.DependsOn(dep("File[a]"))},
		wantWatch: []string{"File[a]"}},
	{name: "legacy IfChanged falls back to deps",
		opts:      []opt.DaemonReloadOption{opt.IfChanged, opt.DependsOn(dep("File[a]"))},
		wantGated: true, wantWatch: []string{"File[a]"}},
	{name: "fallback ignores option order",
		opts:      []opt.DaemonReloadOption{opt.DependsOn(dep("File[a]")), opt.IfChanged},
		wantGated: true, wantWatch: []string{"File[a]"}},
	{name: "IfChanged with WithWatch",
		opts:      []opt.DaemonReloadOption{opt.IfChanged, opt.WithWatch("File[b]", "File[b]")},
		wantGated: true, wantWatch: []string{"File[b]"}},
	{name: "gate ids before WithWatch ids",
		opts:      []opt.DaemonReloadOption{opt.WatchChanges("File[c]"), opt.WithWatch("File[b]")},
		wantGated: true, wantWatch: []string{"File[c]", "File[b]"}},
	{name: "gate ids before WithWatch ids whatever the option order",
		opts:      []opt.DaemonReloadOption{opt.WithWatch("File[b]"), opt.OnChange(dep("File[a]"))},
		wantGated: true, wantWatch: []string{"File[a]", "File[b]"}},
	{name: "WithWatch alone does not arm",
		opts:      []opt.DaemonReloadOption{opt.WithWatch("File[b]")},
		wantWatch: []string{"File[b]"}},
	{name: "last WithWatch replaces earlier ones",
		opts:      []opt.DaemonReloadOption{opt.IfChanged, opt.WithWatch("File[b]"), opt.WithWatch("File[c]")},
		wantGated: true, wantWatch: []string{"File[c]"}},
	{name: "empty WithWatch clears earlier ones, deps fall back",
		opts:      []opt.DaemonReloadOption{opt.IfChanged, opt.WithWatch("File[b]"), opt.WithWatch(), opt.DependsOn(dep("File[a]"))},
		wantGated: true, wantWatch: []string{"File[a]"}},
}

// TestDaemonReloadDraftWatch pins the daemon-reload draft wiring, which
// deliberately differs from the other gated kinds: Watch is the one list
// newReload resolves (see draftWatchCases), and it is recorded even when
// the gate is unarmed. Recorded plans depend on this shape.
func TestDaemonReloadDraftWatch(t *testing.T) {
	for _, tc := range draftWatchCases {
		t.Run(tc.name, func(t *testing.T) {
			d := newReload(tc.opts)
			got := d.planDraft("DaemonReload[system]")
			if got.IfChanged != tc.wantGated || !reflect.DeepEqual(got.Watch, tc.wantWatch) {
				t.Errorf("draft IfChanged=%t Watch=%#v, want %t/%#v",
					got.IfChanged, got.Watch, tc.wantGated, tc.wantWatch)
			}
		})
	}
}

// TestLegacyGateOptionsLowerToUnifiedOp is the equivalence test of the one
// change-gate family: each legacy daemon-reload spelling records exactly
// the plan op of its unified form, so recorded plans stay byte-identical.
func TestLegacyGateOptionsLowerToUnifiedOp(t *testing.T) {
	for _, tc := range []struct {
		name            string
		legacy, unified []opt.DaemonReloadOption
	}{
		{name: "IfChanged+WithWatch is WatchChanges",
			legacy:  []opt.DaemonReloadOption{opt.IfChanged, opt.WithWatch("File[a]", "File[b]")},
			unified: []opt.DaemonReloadOption{opt.WatchChanges("File[a]", "File[b]")}},
		{name: "WithWatch+IfChanged is WatchChanges",
			legacy:  []opt.DaemonReloadOption{opt.WithWatch("File[a]"), opt.IfChanged},
			unified: []opt.DaemonReloadOption{opt.WatchChanges("File[a]")}},
		{name: "WithWatch after the gate ids",
			legacy:  []opt.DaemonReloadOption{opt.WithWatch("File[b]"), opt.IfChanged, opt.OnChange(dep("File[a]"))},
			unified: []opt.DaemonReloadOption{opt.OnChange(dep("File[a]")), opt.WatchChanges("File[b]")}},
		{name: "IfChanged+DependsOn is OnChange",
			legacy:  []opt.DaemonReloadOption{opt.IfChanged, opt.DependsOn(dep("File[a]"), dep("File[b]"))},
			unified: []opt.DaemonReloadOption{opt.OnChange(dep("File[a]"), dep("File[b]"))}},
		{name: "IfChanged+WithWatch+DependsOn is WatchChanges+DependsOn",
			legacy:  []opt.DaemonReloadOption{opt.IfChanged, opt.WithWatch("File[w]"), opt.DependsOn(dep("File[d]"))},
			unified: []opt.DaemonReloadOption{opt.WatchChanges("File[w]"), opt.DependsOn(dep("File[d]"))}},
		{name: "user bus",
			legacy:  []opt.DaemonReloadOption{opt.WithUser, opt.IfChanged, opt.WithWatch("File[a]")},
			unified: []opt.DaemonReloadOption{opt.WithUser, opt.WatchChanges("File[a]")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy, unified := lowerReload(t, tc.legacy), lowerReload(t, tc.unified)
			if !reflect.DeepEqual(legacy, unified) {
				t.Errorf("legacy op %+v, unified op %+v", legacy, unified)
			}
			if !legacy.IfChanged || len(legacy.Watch) == 0 {
				t.Errorf("op %+v is not an armed gate", legacy)
			}
		})
	}
}

// lowerReload builds a daemon-reload from opts and lowers its draft through
// the kind's plan handler, the path a recorded plan takes.
func lowerReload(t *testing.T, opts []opt.DaemonReloadOption) plan.Op {
	t.Helper()
	d := newReload(opts)
	op, err := planHandler{}.ToOp(d.planDraft(d.id()))
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// TestEmptyWithWatchReloadsUnconditionally runs a DaemonReload(WithWatch())
// through Ensure: WithWatch never arms the gate, so it is an ungated
// reload and reloads although nothing changed.
func TestEmptyWithWatchReloadsUnconditionally(t *testing.T) {
	resource.ResetReport()
	t.Cleanup(resource.ResetReport)
	called := false
	sr := &runners.SystemdRunners{Run: func(string, ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	}}
	if err := EnsureWith(sr, opt.WithWatch()); err != nil || !called {
		t.Fatalf("EnsureWith(WithWatch()) = %v, reloaded %t, want an unconditional reload", err, called)
	}
}

// TestReloadArmedWithNothingToWatchRefused pins the behaviour correction of
// b72: a reload applied through Ensure while armed by IfChanged with no
// WithWatch ids and no DependsOn fallback could never reload, so Ensure
// errors instead of silently skipping it. (Present keeps registering such a
// declaration: see TestPresentMergesBareIfChangedLikeBase.)
func TestReloadArmedWithNothingToWatchRefused(t *testing.T) {
	err := Ensure(opt.IfChanged)
	if err == nil || !strings.Contains(err.Error(), "DaemonReload[system]: change gate armed with nothing to watch") {
		t.Fatalf("Ensure(IfChanged) = %v, want the nothing-to-watch refusal", err)
	}
}

// TestPlanHandlerRecordedGate pins the daemon_reload apply side against the
// shared rule (opt.RecordedChangeGate): a gated op with no watch ids is an
// error like for every other gated kind (defence in depth: plan.Apply's
// ValidateChangeGates refuses such an op before any handler runs), a gated
// op fires on a watched change, and an ungated op ignores its recorded
// watch ids.
func TestPlanHandlerRecordedGate(t *testing.T) {
	resource.ResetReport()
	t.Cleanup(resource.ResetReport)
	calls := 0
	sr := &runners.SystemdRunners{Run: func(string, ...string) (string, string, int, error) {
		calls++
		return "", "", 0, nil
	}}
	ctx := plan.ApplyContext{Runners: &runners.Set{Systemd: sr}}

	err := planHandler{}.Apply(plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]", IfChanged: true}, ctx)
	if err == nil || err.Error() != "daemon_reload: if_changed without watch ids" {
		t.Fatalf("gated op without watch = %v, want the if_changed refusal", err)
	}
	gated := plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]", IfChanged: true, Watch: []string{"File[x]"}}
	if err := (planHandler{}).Apply(gated, ctx); err != nil || calls != 0 {
		t.Fatalf("unchanged watch: err %v, %d reloads, want held", err, calls)
	}
	resource.Note("File[x]", resource.StatusChanged)
	if err := (planHandler{}).Apply(gated, ctx); err != nil || calls != 1 {
		t.Fatalf("changed watch: err %v, %d reloads, want one", err, calls)
	}
	ungated := plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]", Watch: []string{"File[never]"}}
	if err := (planHandler{}).Apply(ungated, ctx); err != nil || calls != 2 {
		t.Fatalf("ungated op: err %v, %d reloads, want an unconditional reload", err, calls)
	}
}

// dep is a resource.Dependency naming a single id, for DependsOn options.
type dep string

func (d dep) Dependencies() []string { return []string{string(d)} }

// TestDaemonReloadUnknownWatchSkips pins that a gate watching an id nothing
// ever noted holds the reload and reports it skipped. It uses the direct
// Ensure path on purpose: the plan pre-flight refuses a dangling watch before
// apply, so the gate's own rule is only reachable there.
func TestDaemonReloadUnknownWatchSkips(t *testing.T) {
	resource.ResetReport()
	called := false
	sr := &runners.SystemdRunners{Run: func(string, ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	}}

	if err := EnsureWith(sr, opt.WatchChanges("File[never-noted]")); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("daemon-reload ran although no watched id changed")
	}
	var buf strings.Builder
	resource.PrintSummary(&buf)
	if !strings.Contains(buf.String(), "summary: 0 ok, 0 changed, 1 skipped, 0 would-change") {
		t.Errorf("summary = %q", buf.String())
	}
}
