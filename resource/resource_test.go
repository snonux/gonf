package resource

import (
	"testing"
)

// noopApplier is the registered value of resources these tests only
// register; nothing applies them.
var noopApplier = ApplierFunc(func() error { return nil })

func TestResourceID(t *testing.T) {
	ResetRepository()
	res := Register("File", "/tmp/foo.txt", noopApplier)
	expected := "File[/tmp/foo.txt]"
	if res.ID() != expected {
		t.Errorf("expected ID %s, got %s", expected, res.ID())
	}
}

func TestResourceString(t *testing.T) {
	ResetRepository()
	res := Register("File", "/tmp/foo.txt", noopApplier)
	expected := "File[/tmp/foo.txt]"
	if res.String() != expected {
		t.Errorf("expected String %s, got %s", expected, res.String())
	}
}

func TestNew(t *testing.T) {
	ResetRepository()
	type_ := "File"
	name := "/tmp/foo.txt"
	res := Register(type_, name, noopApplier)

	if res.Type != type_ {
		t.Errorf("expected type %s, got %s", type_, res.Type)
	}
	if res.Name != name {
		t.Errorf("expected name %s, got %s", name, res.Name)
	}
	if res.dependsOn == nil {
		t.Error("expected dependsOn map to be initialized, got nil")
	}
}

func TestRepositoryRegister(t *testing.T) {
	ResetRepository()
	repo := getRepository()
	res := Register("File", "/tmp/foo.txt", noopApplier)

	// First registration already happened in New()
	// But we can try to register again via the repository directly
	if err := repo.register(res); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}

	// Registration of a different resource should succeed
	res2 := Register("File", "/tmp/bar.txt", noopApplier)
	if err := repo.register(res2); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}
}

// TestRollbackToPrunesAdditionsAndKeepsTheSnapshot pins RollbackTo's exact
// contract (tasks ad2/bd2): given a snapshot taken earlier, it removes any
// currently registered resource (and its draft) whose ID is not in that
// snapshot, and leaves everything in the snapshot untouched — including its
// draft, so a caller that only re-registers without re-recording a draft
// does not lose one it already had.
func TestRollbackToPrunesAdditionsAndKeepsTheSnapshot(t *testing.T) {
	ResetRepository()
	Register("File", "/tmp/a", noopApplier)
	RecordPlanDraft(PlanDraft{ID: "File[/tmp/a]", Kind: "file"})
	kept := RegisteredIDs()

	Register("File", "/tmp/b", noopApplier)
	RecordPlanDraft(PlanDraft{ID: "File[/tmp/b]", Kind: "file"})
	if got := RegisteredIDs(); len(got) != 2 {
		t.Fatalf("RegisteredIDs() = %v, want 2 entries before rollback", got)
	}

	RollbackTo(kept)

	got := RegisteredIDs()
	if len(got) != 1 || got[0] != "File[/tmp/a]" {
		t.Fatalf("RegisteredIDs() after RollbackTo = %v, want exactly %v", got, kept)
	}
	drafts := RegisteredPlanDrafts()
	if len(drafts) != 1 || drafts[0].ID != "File[/tmp/a]" {
		t.Fatalf("RegisteredPlanDrafts() after RollbackTo = %v, want only File[/tmp/a]'s draft", drafts)
	}

	// A resource re-registered after being rolled back is unaffected by
	// the earlier rollback (it is simply a new registration).
	Register("File", "/tmp/c", noopApplier)
	if got := RegisteredIDs(); len(got) != 2 {
		t.Fatalf("RegisteredIDs() after a fresh registration = %v, want 2", got)
	}
}

// TestRollbackToEmptySnapshotClearsEverything pins the specific shape
// RecordPlanTo relies on when nothing was registered before it started: a
// nil/empty kept slice prunes every current registration.
func TestRollbackToEmptySnapshotClearsEverything(t *testing.T) {
	ResetRepository()
	Register("File", "/tmp/a", noopApplier)
	Register("File", "/tmp/b", noopApplier)
	if got := RegisteredIDs(); len(got) != 2 {
		t.Fatalf("RegisteredIDs() = %v, want 2 entries before rollback", got)
	}

	RollbackTo(nil)

	if got := RegisteredIDs(); len(got) != 0 {
		t.Fatalf("RegisteredIDs() after RollbackTo(nil) = %v, want none", got)
	}
}
