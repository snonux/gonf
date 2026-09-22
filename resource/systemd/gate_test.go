package systemd

import (
	"reflect"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestDaemonReloadDraftWatch pins the daemon-reload draft wiring, which
// deliberately differs from the other gated kinds: Watch is the one watch
// list every change-gate option fills, falling back to the DependsOn ids
// when no option named any, and it is recorded even when the gate is
// unarmed. Recorded plans depend on this shape.
func TestDaemonReloadDraftWatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      []opt.DaemonReloadOption
		wantGated bool
		wantWatch []string
	}{
		{name: "plain", opts: nil},
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
		{name: "OnChange then WithWatch merge",
			opts:      []opt.DaemonReloadOption{opt.WatchChanges("File[c]"), opt.WithWatch("File[b]")},
			wantGated: true, wantWatch: []string{"File[c]", "File[b]"}},
		// Behaviour correction (b72): WithWatch lowers to WatchChanges, so it
		// arms the gate on its own and repeated calls accumulate (formerly the
		// last WithWatch replaced the earlier ones and it armed nothing).
		{name: "WithWatch alone arms",
			opts:      []opt.DaemonReloadOption{opt.WithWatch("File[b]")},
			wantGated: true, wantWatch: []string{"File[b]"}},
		{name: "WithWatch calls accumulate",
			opts:      []opt.DaemonReloadOption{opt.WithWatch("File[b]"), opt.WithWatch("File[c]", "File[b]")},
			wantGated: true, wantWatch: []string{"File[b]", "File[c]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := newReload(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
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
		{name: "WithWatch is WatchChanges",
			legacy:  []opt.DaemonReloadOption{opt.WithWatch("File[a]")},
			unified: []opt.DaemonReloadOption{opt.WatchChanges("File[a]")}},
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
	d, err := newReload(opts)
	if err != nil {
		t.Fatal(err)
	}
	op, err := planHandler{}.ToOp(d.planDraft(d.id()))
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// TestReloadArmedWithNothingToWatchRefused pins the behaviour correction of
// b72: a reload armed by IfChanged with no WithWatch ids and no DependsOn
// fallback could never reload, so it is refused (Ensure errors, Present
// aborts) instead of being silently skipped on every apply.
func TestReloadArmedWithNothingToWatchRefused(t *testing.T) {
	err := Ensure(opt.IfChanged)
	if err == nil || !strings.Contains(err.Error(), "DaemonReload[system]: change gate armed with nothing to watch") {
		t.Fatalf("Ensure(IfChanged) = %v, want the nothing-to-watch refusal", err)
	}
}

// TestPlanHandlerRecordedGate pins the daemon_reload apply side against the
// shared rule (opt.RecordedChangeGate): a gated op with no watch ids is an
// error like for every other gated kind (formerly it applied as a reload
// that was always skipped), a gated op fires on a watched change, and an
// ungated op ignores its recorded watch ids.
func TestPlanHandlerRecordedGate(t *testing.T) {
	resource.ResetReport()
	t.Cleanup(resource.ResetReport)
	old := runCmd
	t.Cleanup(func() { runCmd = old })
	calls := 0
	runCmd = func(string, ...string) (string, string, int, error) {
		calls++
		return "", "", 0, nil
	}

	err := planHandler{}.Apply(plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]", IfChanged: true}, plan.ApplyContext{})
	if err == nil || err.Error() != "daemon_reload: if_changed without watch ids" {
		t.Fatalf("gated op without watch = %v, want the if_changed refusal", err)
	}
	gated := plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]", IfChanged: true, Watch: []string{"File[x]"}}
	if err := (planHandler{}).Apply(gated, plan.ApplyContext{}); err != nil || calls != 0 {
		t.Fatalf("unchanged watch: err %v, %d reloads, want held", err, calls)
	}
	resource.Note("File[x]", resource.StatusChanged)
	if err := (planHandler{}).Apply(gated, plan.ApplyContext{}); err != nil || calls != 1 {
		t.Fatalf("changed watch: err %v, %d reloads, want one", err, calls)
	}
	ungated := plan.Op{Op: plan.KindDaemonReload, ID: "DaemonReload[system]", Watch: []string{"File[never]"}}
	if err := (planHandler{}).Apply(ungated, plan.ApplyContext{}); err != nil || calls != 2 {
		t.Fatalf("ungated op: err %v, %d reloads, want an unconditional reload", err, calls)
	}
}

// dep is a resource.Dependency naming a single id, for DependsOn options.
type dep string

func (d dep) Dependencies() []string { return []string{string(d)} }

// TestDaemonReloadUnknownWatchSkips pins that a gate watching an id nothing
// ever noted holds the reload and reports it skipped.
func TestDaemonReloadUnknownWatchSkips(t *testing.T) {
	resource.ResetRepository()
	old := runCmd
	t.Cleanup(func() { runCmd = old })
	called := false
	runCmd = func(string, ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	}

	Present(opt.WatchChanges("File[never-noted]"))
	if err := resource.Apply(); err != nil {
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
