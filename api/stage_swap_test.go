package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
)

// stagedBlobOfEachKind is a staging directory holding one blob of each kind a
// commit copies (a single file and a tree), plus the ops that reference them.
func stagedBlobOfEachKind(t *testing.T) (stage string, ops []plan.Op) {
	t.Helper()
	stage = t.TempDir()
	tree := filepath.Join(stage, "blobs", "t")
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(stage, "blobs", "b"), []byte("secret blob"))
	mustWrite(t, filepath.Join(tree, "f"), []byte("secret tree file"))
	return stage, []plan.Op{{Blob: "blobs/b"}, {Blob: "blobs/t"}}
}

// oncePlanDirVerified runs fn at the planDirVerified seam (after the commit
// verified the operator's plan directory, before the writability re-check and
// the first blob write) and restores the seam when the test ends.
func oncePlanDirVerified(t *testing.T, fn func(planDir string)) {
	t.Helper()
	prev := planDirVerified
	planDirVerified = func(planDir string, _ *plan.Store) { fn(planDir) }
	t.Cleanup(func() { planDirVerified = prev })
}

// TestCommitStagedBlobsChecksWritabilityOfHeldDir: the verified plan
// directory is made read-only and its path swapped for a symlink to a
// writable directory after OpenSecureStore. The writability re-check must ask
// about the held (read-only) directory, not the path, and refuse with the
// usual "cannot write to" wording before any blob is written: the held
// directory's writable blobs/ stays untouched and the link target stays empty.
func TestCommitStagedBlobsChecksWritabilityOfHeldDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root passes access(2) write checks on read-only directories")
	}
	stage, ops := stagedBlobOfEachKind(t)
	planDir := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "out"), 0o700)
	blobs := testutil.MkdirMode(t, filepath.Join(planDir, "blobs"), 0o700)
	writable := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "writable"), 0o700)
	var moved string
	oncePlanDirVerified(t, func(dir string) {
		moved = swapDirForSymlink(t, dir, writable)
		if err := os.Chmod(moved, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(moved, 0o700) })
	})
	before := testutil.Snapshot(t, blobs)
	err := commitStagedBlobs(ops, stage, planDir)
	if err == nil || !strings.Contains(err.Error(), "RecordPlan: plan dir: cannot write to ") ||
		!strings.Contains(err.Error(), "run chmod u+w on it") {
		t.Fatalf("commitStagedBlobs = %v, want the \"cannot write to\" refusal of the held directory", err)
	}
	testutil.RequireUnchanged(t, before, filepath.Join(moved, "blobs"))
	requireDirEmpty(t, writable)
}

// TestCommitStagedBlobsClosesTheStore: the held plan directory descriptor is
// released when the commit returns, on success and on a refusal alike; a
// closed store refuses writes (it never falls back to the path).
func TestCommitStagedBlobsClosesTheStore(t *testing.T) {
	stage, ops := stagedBlobOfEachKind(t)
	for name, planDir := range map[string]string{
		"success": filepath.Join(t.TempDir(), "out"),
		"refusal": testutil.MkdirMode(t, filepath.Join(t.TempDir(), "ro"), 0o500),
	} {
		t.Run(name, func(t *testing.T) {
			var held *plan.Store
			prev := planDirVerified
			planDirVerified = func(_ string, dest *plan.Store) { held = dest }
			t.Cleanup(func() { planDirVerified = prev })
			_ = commitStagedBlobs(ops, stage, planDir)
			if held == nil {
				t.Fatal("the commit never opened the plan directory")
			}
			if _, err := held.WriteFile("late", []byte("x")); err == nil || !strings.Contains(err.Error(), "closed") {
				t.Fatalf("write after the commit returned = %v, want a closed-store refusal", err)
			}
		})
	}
}

// swapDirForSymlink moves dir aside (to dir+".orig") and puts a symlink to
// target in its place. It returns the directory's new name.
func swapDirForSymlink(t *testing.T, dir, target string) string {
	t.Helper()
	moved := dir + ".orig"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	return moved
}

// TestCommitStagedBlobsIgnoresPlanDirSwappedAfterCheck: the operator's plan
// directory swapped for a symlink after the commit verified it must not
// redirect any blob (they carry secret material) into the link's target. The
// commit writes through the descriptor of the directory it verified, so the
// blobs land there (now <dir>.orig), and the target stays empty.
func TestCommitStagedBlobsIgnoresPlanDirSwappedAfterCheck(t *testing.T) {
	stage, ops := stagedBlobOfEachKind(t)
	planDir := filepath.Join(t.TempDir(), "out")
	elsewhere := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
	var moved string
	oncePlanDirVerified(t, func(dir string) { moved = swapDirForSymlink(t, dir, elsewhere) })
	if err := commitStagedBlobs(ops, stage, planDir); err != nil {
		t.Fatalf("commitStagedBlobs: %v", err)
	}
	requireDirEmpty(t, elsewhere)
	if got := mustRead(t, filepath.Join(moved, "blobs", "b")); got != "secret blob" {
		t.Fatalf("file blob in the verified dir = %q", got)
	}
	if got := mustRead(t, filepath.Join(moved, "blobs", "t", "f")); got != "secret tree file" {
		t.Fatalf("tree blob in the verified dir = %q", got)
	}
}

// TestCommitStagedBlobsRefusesBlobsSwappedAfterCheck: a blobs/ swapped for a
// symlink after the plan directory was verified is refused (blobs/ is opened
// without following a symlink) and nothing is written through it.
func TestCommitStagedBlobsRefusesBlobsSwappedAfterCheck(t *testing.T) {
	stage, ops := stagedBlobOfEachKind(t)
	planDir := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "out"), 0o700)
	testutil.MkdirMode(t, filepath.Join(planDir, "blobs"), 0o700)
	elsewhere := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
	oncePlanDirVerified(t, func(dir string) { swapDirForSymlink(t, filepath.Join(dir, "blobs"), elsewhere) })
	requireSinglePrefixRefusal(t, commitStagedBlobs(ops, stage, planDir), `RecordPlan: write blob "blobs/b"`, "is a symlink")
	requireDirEmpty(t, elsewhere)
}

// requireDirEmpty fails unless dir exists and is empty.
func requireDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s holds %v, want nothing written through the symlink", dir, entries)
	}
}
