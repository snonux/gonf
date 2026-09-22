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

// TestQuoteGlobInvalidUTF8 pins that quoteGlob escapes bytes, not runes: a
// Unix path is an arbitrary byte string and need not be valid UTF-8, so
// ranging over runes would rewrite an invalid byte as the 3-byte
// utf8.RuneError replacement character, changing the pattern's length and
// making it name a different (nonexistent) path (task kc2 — a regression in
// task sb2, which added quoteGlob).
func TestQuoteGlobInvalidUTF8(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// "caf" + an invalid UTF-8 continuation byte, unpaired.
	odd := filepath.Join(dir, "caf\xe9")
	if err := os.MkdirAll(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odd, "x.sh"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	quoted := quoteGlob(odd)
	if quoted != odd {
		t.Fatalf("quoteGlob(%q) = %q, want it unchanged (no metacharacters, so byte-for-byte identical)", odd, quoted)
	}
	matches, err := filepath.Glob(filepath.Join(quoted, "*"))
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

// TestSyncDirGlobPruneSurvivesNonUTF8PlanDir pins the exact data-loss
// scenario task kc2 fixed: a plan directory whose path is not valid UTF-8
// (Unix paths are arbitrary byte strings — nothing requires this) must not
// make quoteGlob rebuild a pattern that resolves to nothing. Under the bug,
// an invalid byte in the plan dir made the rebuilt glob match ZERO entries
// even though the blob has real content, so WithSourceGlob installed
// nothing and WithPrune's keep-set was empty — wiping every regular file
// directly under the destination, including ones that should have been
// refreshed from the blob (not just leaving an unrelated stale file behind,
// which is what makes this scenario distinct from
// TestSyncDirGlobPruneLeavesSubdirectories's ordinary prune case).
func TestSyncDirGlobPruneSurvivesNonUTF8PlanDir(t *testing.T) {
	resource.ResetRepository()
	planDir := filepath.Join(t.TempDir(), "plans-caf\xe9")
	blobRef, err := plan.BlobRefFor("scripts")
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
	if err := os.WriteFile(filepath.Join(dst, "a.sh"), []byte("old\n"), 0o644); err != nil {
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

	// The bug's signature: under it, a.sh (which DOES match a blob entry)
	// would ALSO be pruned, because the broken pattern matched nothing at
	// all, leaving the keep-set empty rather than {a.sh}.
	if got, err := os.ReadFile(filepath.Join(dst, "a.sh")); err != nil || string(got) != "new\n" {
		t.Errorf("a.sh = %q, %v; want the blob's content installed, not pruned", got, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "keepdir")); err != nil {
		t.Errorf("unmanaged subdirectory was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.sh")); !os.IsNotExist(err) {
		t.Errorf("non-matching regular file survived: %v", err)
	}
}
