package configset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// TestPresentRefusedSetRegistersNoMembers pins task ig2's fix: sf2 gated
// every LEAF resource.Register call site on its own ok before recording a
// plan draft, but Present's member loop registered and drafted each member
// unconditionally, regardless of whether the set's own "ConfigSet[app]"
// registration collided and was refused. Two ConfigSet("app", ...)
// declarations with different members reproduce the collision: the second
// is refused (same ID as the first), and before this fix the refused
// declaration's own member ("app/b") still ended up registered with a
// draft -- AGENTS.md's "Never register a refused declaration", violated for
// a composite resource specifically (a leaf resource has only one Register
// call, so sf2's per-call gate already covered it there).
func TestPresentRefusedSetRegistersNoMembers(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)

	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.conf")
	pathB := filepath.Join(dir, "b.conf")

	Present("app", opt.ConfigFile("a", pathA, opt.WithContent("a\n")), opt.WithSetValidation("true", nil))
	Present("app", opt.ConfigFile("b", pathB, opt.WithContent("b\n")), opt.WithSetValidation("true", nil))

	if err := declerr.First(); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate ConfigSet declaration reported %v, want already registered", err)
	}

	got := resource.RegisteredIDs()
	// RegisteredIDs is sorted lexicographically: "ConfigSetMember[...]"
	// sorts before "ConfigSet[app]" because 'M' < '[' byte-wise.
	want := []string{"ConfigSetMember[app/a]", "ConfigSet[app]"}
	if len(got) != len(want) {
		t.Fatalf("registered IDs = %v, want %v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("registered IDs = %v, want %v", got, want)
		}
	}
	for _, id := range got {
		if id == "ConfigSetMember[app/b]" {
			t.Fatalf("registered IDs = %v: the refused second declaration's member must not register", got)
		}
	}
}

// TestPresentRefusedSetThenResetDeclarationErrorAppliesCleanly reproduces
// the documented safe remedy from task ig2's annotation: after the
// collision above, ResetDeclarationError (clear-and-continue, no
// ResetRepository -- the safe remedy for a collided-ID class per
// resource.go's doc comment) must leave a cleanly applicable registered
// state -- the FIRST declaration's set and member only, with no partial
// apply and no confusing mid-stream "has not been applied before its
// member handle" error caused by a leftover member from the refused
// second declaration.
func TestPresentRefusedSetThenResetDeclarationErrorAppliesCleanly(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)

	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.conf")
	pathB := filepath.Join(dir, "b.conf")

	Present("app", opt.ConfigFile("a", pathA, opt.WithContent("a\n")), opt.WithSetValidation("true", nil))
	Present("app", opt.ConfigFile("b", pathB, opt.WithContent("b\n")), opt.WithSetValidation("true", nil))

	if declerr.First() == nil {
		t.Fatal("expected the duplicate ConfigSet declaration to report a collision")
	}
	// The genuinely-safe collision remedy (task vf2's doc comment on
	// ResetDeclarationError, amended by ig2 for the composite case): safe
	// here because configset.Present now gates its member loop on the
	// set's own ok, so the refused second declaration left no member
	// behind for the discarded error to hide.
	if discarded := resource.ResetDeclarationError(); discarded == nil {
		t.Fatal("ResetDeclarationError returned nil, want the discarded collision error")
	}

	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply() after ResetDeclarationError = %v, want nil (no partial apply, no stray member)", err)
	}

	got, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read %s: %v", pathA, err)
	}
	if string(got) != "a\n" {
		t.Fatalf("content of %s = %q, want %q", pathA, got, "a\n")
	}
	if _, err := os.Stat(pathB); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want not exist (the refused declaration's member must never publish)", pathB, err)
	}
}
