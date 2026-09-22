package systemd

import (
	"reflect"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

// registeredDraft returns the stored plan draft for id in the current scope.
func registeredDraft(t *testing.T, id string) resource.PlanDraft {
	t.Helper()
	for _, d := range resource.RegisteredPlanDrafts() {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("no registered draft %s", id)
	return resource.PlanDraft{}
}

// dummy registers a no-op resource so reloads can depend on real IDs under
// the legacy repository apply path.
func dummy(name string) resource.Resource {
	return resource.Register("Dummy", name, resource.ApplierFunc(func() error { return nil }))
}

// TestPresentMergesSameBusDeclarations pins that a second declaration on the
// same bus returns the first reload and folds its watches and deps into it:
// the stored draft (api.Apply) and the repository edges (legacy apply) both
// see the union, and only one daemon-reload is registered.
func TestPresentMergesSameBusDeclarations(t *testing.T) {
	resource.ResetRepository()
	a, b := dummy("a"), dummy("b")

	first := Present(opt.OnChange(a))
	second := Present(opt.OnChange(b))
	if first.ID() != "DaemonReload[system]" || second.ID() != first.ID() {
		t.Fatalf("ids = %s, %s, want both DaemonReload[system]", first.ID(), second.ID())
	}
	d := registeredDraft(t, "DaemonReload[system]")
	want := []string{"Dummy[a]", "Dummy[b]"}
	if !d.IfChanged || !reflect.DeepEqual(d.Watch, want) || !reflect.DeepEqual(d.Deps, want) {
		t.Fatalf("merged draft = %#v, want armed, watch and deps %v", d, want)
	}
	if got := resource.RegisteredIDs(); !reflect.DeepEqual(got, []string{"DaemonReload[system]", "Dummy[a]", "Dummy[b]"}) {
		t.Fatalf("registered = %v, want one reload", got)
	}
	// The applier the legacy path runs is the first declaration, merged.
	res, reg, ok := resource.Registered("DaemonReload[system]")
	if !ok || !reflect.DeepEqual(reg.(*DaemonReloadResource).Watch, want) {
		t.Fatalf("registered applier not merged: %#v", reg)
	}
	if !reflect.DeepEqual(res.Dependencies(), []string{"DaemonReload[system]"}) {
		t.Fatalf("registered resource = %v", res)
	}
}

// TestPresentMergeOrdersAfterWatchOnlyIDs pins that a folded-in declaration
// that only watches an id (legacy WithWatch + IfChanged, the WatchChanges
// alias) still orders the merged
// reload after it, while a watched id from outside this scope adds no edge.
func TestPresentMergeOrdersAfterWatchOnlyIDs(t *testing.T) {
	resource.ResetRepository()
	a, _ := dummy("a"), dummy("b")
	Present(opt.OnChange(a))
	Present(opt.WithWatch("Dummy[b]", "Elsewhere[x]"), opt.IfChanged)

	d := registeredDraft(t, "DaemonReload[system]")
	if !reflect.DeepEqual(d.Deps, []string{"Dummy[a]", "Dummy[b]"}) {
		t.Fatalf("merged deps = %v, want the watched Dummy[b] as an ordering dep and no Elsewhere[x]", d.Deps)
	}
	if !reflect.DeepEqual(d.Watch, []string{"Dummy[a]", "Dummy[b]", "Elsewhere[x]"}) {
		t.Fatalf("merged watch = %v", d.Watch)
	}
}

// TestPresentKeepsBusesSeparate pins the no-merge case: the system and user
// buses are distinct singletons.
func TestPresentKeepsBusesSeparate(t *testing.T) {
	resource.ResetRepository()
	a := dummy("a")
	sys := Present(opt.OnChange(a))
	usr := Present(opt.OnChange(a), opt.WithUser)
	if sys.ID() != "DaemonReload[system]" || usr.ID() != "DaemonReload[user]" {
		t.Fatalf("ids = %s, %s", sys.ID(), usr.ID())
	}
	if !registeredDraft(t, "DaemonReload[user]").User {
		t.Fatal("user reload lost its bus")
	}
}

// TestMergedGateSemantics pins the gate rules of a merge: armed only when
// both declarations are (an unconditional reload absorbs a gated one), and a
// legacy IfChanged relying on the DependsOn fallback keeps watching its deps
// once explicit watch ids join.
func TestMergedGateSemantics(t *testing.T) {
	gated := &DaemonReloadResource{}
	gated.SetChangeWatch([]string{"File[/u]"})
	always := &DaemonReloadResource{}
	always.AddDependency("File[/v]")

	if m := gated.merged(always); m.Gated {
		t.Fatalf("gated+unconditional merge stayed armed: %#v", m)
	}
	if m := always.merged(gated); m.Gated {
		t.Fatalf("unconditional+gated merge armed: %#v", m)
	}

	// newReload resolves the legacy DependsOn fallback into Watch, as
	// Present does before any merge.
	addDep := opt.ToDaemonReloadOptions(func(target any) {
		target.(*DaemonReloadResource).AddDependency("File[/legacy]")
	})
	legacy := newReload(append(addDep, opt.IfChanged))
	m := legacy.merged(gated)
	if !m.Gated || !reflect.DeepEqual(m.Watch, []string{"File[/legacy]", "File[/u]"}) {
		t.Fatalf("legacy+OnChange merge = gated %v watch %v, want armed on both", m.Gated, m.Watch)
	}
	if !reflect.DeepEqual(legacy.Watch, []string{"File[/legacy]"}) {
		t.Fatalf("merged mutated the receiver: %v", legacy.Watch)
	}
}

// TestPresentMergedReloadAppliesOnSecondInput runs the merged reload through
// the legacy repository path: a change of only the second declaration's
// input must fire the reload, which is what the merge adds.
func TestPresentMergedReloadAppliesOnSecondInput(t *testing.T) {
	resource.ResetRepository()
	resource.ResetReport()
	t.Cleanup(resource.ResetReport)
	a := dummy("a")
	b := resource.Register("Dummy", "b", resource.ApplierFunc(func() error {
		resource.Note("Dummy[b]", resource.StatusChanged)
		return nil
	}))
	Present(opt.OnChange(a))
	Present(opt.OnChange(b))

	old := runCmd
	t.Cleanup(func() { runCmd = old })
	var saw []string
	runCmd = func(name string, args ...string) (string, string, int, error) {
		saw = append(saw, name+" "+strings.Join(args, " "))
		return "", "", 0, nil
	}
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if len(saw) != 1 || saw[0] != "systemctl daemon-reload" {
		t.Fatalf("systemctl calls = %v, want one daemon-reload fired by the second input", saw)
	}
}

// TestPresentMergesBareIfChangedLikeBase pins that a bare DaemonReload
// (IfChanged) — armed with nothing to watch — merges with a same-bus
// declaration in either order, as it did before b72, instead of aborting:
// only a reload that stays unwatchable is refused, by the plan pre-flight.
// The expected drafts are the ones the pre-b72 code records.
func TestPresentMergesBareIfChangedLikeBase(t *testing.T) {
	for _, tc := range []struct {
		name      string
		declare   func()
		wantGated bool
		wantWatch []string
		wantDeps  []string
	}{
		{name: "WatchChanges then IfChanged", declare: func() {
			Present(opt.WatchChanges("Dummy[a]"))
			Present(opt.IfChanged)
		}, wantGated: true, wantWatch: []string{"Dummy[a]"}},
		{name: "IfChanged then WatchChanges", declare: func() {
			Present(opt.IfChanged)
			Present(opt.WatchChanges("Dummy[a]"))
		}, wantGated: true, wantWatch: []string{"Dummy[a]"}, wantDeps: []string{"Dummy[a]"}},
		{name: "plain then IfChanged", declare: func() {
			Present()
			Present(opt.IfChanged)
		}},
		{name: "bare IfChanged alone registers", declare: func() {
			Present(opt.IfChanged)
		}, wantGated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetRepository()
			t.Cleanup(resource.ResetRepository)
			dummy("a")
			tc.declare()
			d := registeredDraft(t, "DaemonReload[system]")
			if d.IfChanged != tc.wantGated || !reflect.DeepEqual(d.Watch, tc.wantWatch) || !reflect.DeepEqual(d.Deps, tc.wantDeps) {
				t.Fatalf("draft IfChanged=%t Watch=%#v Deps=%#v, want %t/%#v/%#v",
					d.IfChanged, d.Watch, d.Deps, tc.wantGated, tc.wantWatch, tc.wantDeps)
			}
		})
	}
}
