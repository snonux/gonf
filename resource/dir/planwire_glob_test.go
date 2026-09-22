package dir

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestSyncDirHandlerToOpMarksGlobFlavor pins the record side of the sb2 fix:
// a draft carrying a SourceGlob lowers to a sync_dir op with glob set, a
// tree draft to one without it. Without the flag the destination cannot tell
// the flattened glob blob from a packaged tree and prunes with tree
// semantics.
func TestSyncDirHandlerToOpMarksGlobFlavor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		draft resource.PlanDraft
		want  bool
	}{
		{"glob", resource.PlanDraft{ID: "Directory[/dst]", Path: "/dst", SourceGlob: "/src/*", SourceDir: "/src"}, true},
		{"tree", resource.PlanDraft{ID: "Directory[/dst]", Path: "/dst", SourceDir: "/src"}, false},
	}
	for _, tc := range cases {
		op, err := syncDirHandler{}.ToOp(tc.draft)
		if err != nil {
			t.Fatalf("%s: ToOp: %v", tc.name, err)
		}
		if op.Glob != tc.want {
			t.Errorf("%s: op.Glob = %t, want %t", tc.name, op.Glob, tc.want)
		}
	}
}

// TestQuoteGlob pins that a blob path is matched literally: blob directory
// names are derived from the destination's basename, which may hold glob
// metacharacters (e.g. "Directory[/etc/conf[1]]"), and an unquoted prefix
// would then select the wrong directory or nothing at all.
func TestQuoteGlob(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	odd := filepath.Join(dir, "b[l]o*b?s")
	if err := os.MkdirAll(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odd, "x.sh"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(quoteGlob(odd), "*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 1 || filepath.Base(matches[0]) != "x.sh" {
		t.Fatalf("matches = %v, want exactly the entry of %s", matches, odd)
	}
}

// TestSyncDirGlobPruneLeavesSubdirectories applies a glob sync_dir op
// through the handler itself (the destination side of a plan), sourced from
// a blob under an operator-chosen plan directory (quoteGlob's actual risk
// vector — sanitizeBlobName already strips glob metacharacters from the
// blob directory name itself, see TestQuoteGlob for that path). The
// unmanaged subdirectory and the non-matching regular file must follow glob
// prune semantics: the directory stays, the file goes.
func TestSyncDirGlobPruneLeavesSubdirectories(t *testing.T) {
	resource.ResetRepository()
	planDir := t.TempDir()
	blobRef, err := plan.BlobRefFor("scripts[1]")
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(planDir, filepath.FromSlash(blobRef))
	if err := os.MkdirAll(blob, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blob, "a.sh"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(filepath.Join(dst, "keepdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "stale.sh"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	op := plan.Op{Op: plan.KindSyncDir, ID: "Directory[" + dst + "]", Path: dst,
		Blob: blobRef, Glob: true, Prune: true, Mode: "0700", FileMode: "0600"}
	if err := (syncDirHandler{}).Apply(op, plan.ApplyContext{PlanDir: planDir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "keepdir")); err != nil {
		t.Errorf("unmanaged subdirectory was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.sh")); !os.IsNotExist(err) {
		t.Errorf("non-matching regular file survived: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "a.sh")); err != nil || string(got) != "new\n" {
		t.Errorf("a.sh = %q, %v; want the blob's content", got, err)
	}
}
