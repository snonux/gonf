package resource

import (
	"errors"
	"strings"
	"testing"
)

// noop returns a placeholder registered value for tests that only care about
// dependency edges and drafts, never about applying anything.
func noop() any { return nil }

// TestAmendRegisteredAddsEdgesAndDraft pins the happy path: the new edges
// and draft are stored, and the record-mode sink sees the draft.
func TestAmendRegisteredAddsEdgesAndDraft(t *testing.T) {
	ResetRepository()
	t.Cleanup(func() { ResetRepository(); SetPlanDraftAmender(nil) })
	Register("X", "a", noop())
	target := Register("R", "r", noop())
	var sunk []PlanDraft
	SetPlanDraftAmender(func(d PlanDraft) error { sunk = append(sunk, d); return nil })

	if err := AmendRegistered(PlanDraft{ID: "R[r]", Kind: "k"}, "X[a]"); err != nil {
		t.Fatalf("AmendRegistered: %v", err)
	}
	if got := target.sortedDependsOn(); len(got) != 1 || got[0] != "X[a]" {
		t.Fatalf("edges = %v, want [X[a]] visible through the returned value", got)
	}
	if len(sunk) != 1 || RegisteredPlanDrafts()[0].Kind != "k" {
		t.Fatalf("sink=%v drafts=%v", sunk, RegisteredPlanDrafts())
	}
}

// TestAmendRegisteredRefusesCycle is the negative case behind the
// SystemdUnits "input depends on an earlier composition" refusal: an edge
// that would close a cycle is refused before the sink runs or anything is
// stored.
func TestAmendRegisteredRefusesCycle(t *testing.T) {
	ResetRepository()
	t.Cleanup(func() { ResetRepository(); SetPlanDraftAmender(nil) })
	target := Register("R", "r", noop())
	Register("T", "t", noop(), "R[r]")
	Register("X", "b", noop(), "T[t]") // b -> t -> r
	SetPlanDraftAmender(func(PlanDraft) error { t.Fatal("sink ran for a refused amendment"); return nil })

	err := AmendRegistered(PlanDraft{ID: "R[r]"}, "X[b]")
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err = %v, want a cycle refusal", err)
	}
	if got := target.sortedDependsOn(); len(got) != 0 {
		t.Fatalf("refused amendment added edges: %v", got)
	}
}

// TestAmendRegisteredRefusesSelfDependency covers the direct self edge (a
// declaration passing a composition that contains the target to FanIn or
// DependsOn): it is refused with its own wording, before the reachability
// walk (which never visits the start id itself) could miss it, and even when
// listed after an unrelated dep.
func TestAmendRegisteredRefusesSelfDependency(t *testing.T) {
	ResetRepository()
	t.Cleanup(func() { ResetRepository(); SetPlanDraftAmender(nil) })
	Register("X", "a", noop())
	target := Register("R", "r", noop())
	SetPlanDraftAmender(func(PlanDraft) error { t.Fatal("sink ran for a refused amendment"); return nil })

	err := AmendRegistered(PlanDraft{ID: "R[r]"}, "X[a]", "R[r]")
	if err == nil || !strings.Contains(err.Error(), "make R[r] depend on itself") {
		t.Fatalf("err = %v, want the self-dependency refusal", err)
	}
	if strings.Contains(err.Error(), "already depends on") {
		t.Fatalf("self-dependency reported with the cycle wording: %v", err)
	}
	if got := target.sortedDependsOn(); len(got) != 0 {
		t.Fatalf("refused amendment added edges: %v", got)
	}
}

// TestAmendRegisteredSinkErrorChangesNothing: a refusal from the record-mode
// sink (e.g. a when-block boundary) leaves the repository untouched.
func TestAmendRegisteredSinkErrorChangesNothing(t *testing.T) {
	ResetRepository()
	t.Cleanup(func() { ResetRepository(); SetPlanDraftAmender(nil) })
	Register("X", "a", noop())
	target := Register("R", "r", noop())
	RecordPlanDraft(PlanDraft{ID: "R[r]", Kind: "original"})
	refuse := errors.New("boundary")
	SetPlanDraftAmender(func(PlanDraft) error { return refuse })

	if err := AmendRegistered(PlanDraft{ID: "R[r]", Kind: "amended"}, "X[a]"); !errors.Is(err, refuse) {
		t.Fatalf("err = %v, want the sink's refusal", err)
	}
	if got := target.sortedDependsOn(); len(got) != 0 {
		t.Fatalf("refused amendment added edges: %v", got)
	}
	// The stored draft must be replaced only after the sink accepted it.
	if drafts := RegisteredPlanDrafts(); len(drafts) != 1 || drafts[0].Kind != "original" {
		t.Fatalf("refused amendment replaced the stored draft: %#v", drafts)
	}
}

// TestRegisteredWatchTargetsExpandsDirectories pins the AnyChanged prefix
// rule at merge time: a watched Directory[p] also yields every registered
// File[p] / File[p/…], but not a sibling sharing the prefix (File[p2/…]);
// unregistered watch ids yield nothing, and duplicates collapse.
func TestRegisteredWatchTargetsExpandsDirectories(t *testing.T) {
	ResetRepository()
	t.Cleanup(ResetRepository)
	for _, name := range []string{"/d/b.conf", "/d/a.conf", "/d2/x", "/other"} {
		Register("File", name, noop())
	}
	Register("Directory", "/d", noop())

	got := RegisteredWatchTargets("Directory[/d]", "File[/other]", "File[/d/a.conf]", "Elsewhere[x]")
	want := []string{"Directory[/d]", "File[/d/a.conf]", "File[/d/b.conf]", "File[/other]"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("RegisteredWatchTargets = %v, want %v", got, want)
	}
}

// TestAmendRegisteredUnknownID refuses an ID outside the current scope.
func TestAmendRegisteredUnknownID(t *testing.T) {
	ResetRepository()
	t.Cleanup(ResetRepository)
	if err := AmendRegistered(PlanDraft{ID: "R[r]"}); err == nil {
		t.Fatal("amending an unregistered id succeeded")
	}
	if _, _, ok := Registered("R[r]"); ok {
		t.Fatal("Registered found an unregistered id")
	}
}
