package resource

import (
	"testing"
)

// noopRegistered is the registered value of resources these tests only
// register; nothing applies them.
var noopRegistered = func() error { return nil }

func TestResourceID(t *testing.T) {
	ResetRepository()
	res, _ := Register("File", "/tmp/foo.txt", noopRegistered)
	expected := "File[/tmp/foo.txt]"
	if res.ID() != expected {
		t.Errorf("expected ID %s, got %s", expected, res.ID())
	}
}

func TestResourceString(t *testing.T) {
	ResetRepository()
	res, _ := Register("File", "/tmp/foo.txt", noopRegistered)
	expected := "File[/tmp/foo.txt]"
	if res.String() != expected {
		t.Errorf("expected String %s, got %s", expected, res.String())
	}
}

func TestNew(t *testing.T) {
	ResetRepository()
	type_ := "File"
	name := "/tmp/foo.txt"
	res, _ := Register(type_, name, noopRegistered)

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
	res, _ := Register("File", "/tmp/foo.txt", noopRegistered)

	// First registration already happened in New()
	// But we can try to register again via the repository directly
	if err := repo.register(res); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}

	// Registration of a different resource should succeed
	res2, _ := Register("File", "/tmp/bar.txt", noopRegistered)
	if err := repo.register(res2); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}
}

// TestSnapshotRepositoryRestoresExactlyThePreSnapshotState pins
// SnapshotRepository's exact contract (tasks ad2/bd2/id2): after the
// snapshot, the repository is empty (a fresh scratch space); registrations
// made after the snapshot do not appear once restore runs, and everything
// that was registered before the snapshot — including its draft — comes
// back exactly as it was.
func TestSnapshotRepositoryRestoresExactlyThePreSnapshotState(t *testing.T) {
	ResetRepository()
	Register("File", "/tmp/a", noopRegistered)
	RecordPlanDraft(PlanDraft{ID: "File[/tmp/a]", Kind: "file"})

	restore := SnapshotRepository()
	if got := RegisteredIDs(); len(got) != 0 {
		t.Fatalf("RegisteredIDs() right after the snapshot = %v, want none (a fresh scratch space)", got)
	}

	Register("File", "/tmp/b", noopRegistered)
	RecordPlanDraft(PlanDraft{ID: "File[/tmp/b]", Kind: "file"})
	if got := RegisteredIDs(); len(got) != 1 || got[0] != "File[/tmp/b]" {
		t.Fatalf("RegisteredIDs() before restore = %v, want only File[/tmp/b]", got)
	}

	restore()

	got := RegisteredIDs()
	if len(got) != 1 || got[0] != "File[/tmp/a]" {
		t.Fatalf("RegisteredIDs() after restore = %v, want exactly [File[/tmp/a]]", got)
	}
	drafts := RegisteredPlanDrafts()
	if len(drafts) != 1 || drafts[0].ID != "File[/tmp/a]" {
		t.Fatalf("RegisteredPlanDrafts() after restore = %v, want only File[/tmp/a]'s draft", drafts)
	}
}

// TestSnapshotRepositoryRestoresTheOriginalOnAnIDCollision pins the exact
// bug task id2 found in an earlier, ID-based RollbackTo(kept []string):
// pruning down to a set of NAMES could keep a same-ID registration made
// AFTER the snapshot — with its own, new value — instead of restoring the
// original. Swapping the whole repository pointer back cannot do that: the
// post-snapshot registration of the same ID must simply not exist once
// restore runs, and the original resource's identity (here, distinguished
// by which func() error closure it wraps) must be the one that comes back.
func TestSnapshotRepositoryRestoresTheOriginalOnAnIDCollision(t *testing.T) {
	ResetRepository()
	var originalRan, collidingRan bool
	Register("File", "/tmp/a", func() error { originalRan = true; return nil })
	RecordPlanDraft(PlanDraft{ID: "File[/tmp/a]", Kind: "file", Path: "original"})

	restore := SnapshotRepository()
	// Same ID as before the snapshot, but a different value and draft —
	// standing in for a task body that happens to redeclare the same
	// resource name (e.g. via a shared helper both an outer top-level
	// declaration and an unrelated task body call).
	Register("File", "/tmp/a", func() error { collidingRan = true; return nil })
	RecordPlanDraft(PlanDraft{ID: "File[/tmp/a]", Kind: "file", Path: "colliding"})

	restore()

	drafts := RegisteredPlanDrafts()
	if len(drafts) != 1 || drafts[0].Path != "original" {
		t.Fatalf("RegisteredPlanDrafts() after restore = %v, want the ORIGINAL draft (Path \"original\"), not the colliding one", drafts)
	}
	_, registered, ok := Registered("File[/tmp/a]")
	if !ok {
		t.Fatal("Registered(File[/tmp/a]) = false after restore, want true")
	}
	work, ok := registered.(func() error)
	if !ok {
		t.Fatalf("Registered(File[/tmp/a]) = %#v, want a func() error", registered)
	}
	if err := work(); err != nil {
		t.Fatal(err)
	}
	if !originalRan || collidingRan {
		t.Fatalf("originalRan=%v collidingRan=%v, want the restored resource to be the ORIGINAL one, not the colliding registration", originalRan, collidingRan)
	}
}

// TestSnapshotRepositoryOfAnEmptyRepositoryRestoresEmpty pins the specific
// shape RecordPlanTo relies on when nothing was registered before it
// started: restore leaves the repository empty, the same as
// RollbackTo(nil) used to.
func TestSnapshotRepositoryOfAnEmptyRepositoryRestoresEmpty(t *testing.T) {
	ResetRepository()
	restore := SnapshotRepository()
	Register("File", "/tmp/a", noopRegistered)
	Register("File", "/tmp/b", noopRegistered)
	if got := RegisteredIDs(); len(got) != 2 {
		t.Fatalf("RegisteredIDs() before restore = %v, want 2 entries", got)
	}

	restore()

	if got := RegisteredIDs(); len(got) != 0 {
		t.Fatalf("RegisteredIDs() after restore = %v, want none (nothing was registered before the snapshot)", got)
	}
}
