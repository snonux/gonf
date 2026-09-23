package dir

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	. "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
)

// dirUIDGid returns the directory's owning uid/gid via syscall.Stat_t. Skips
// the test when the platform does not expose Stat_t (repo targets unix only).
func dirUIDGid(t *testing.T, path string) (int, int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skipf("no syscall.Stat_t on this platform")
	}
	return int(st.Uid), int(st.Gid)
}

// currentOwnerForTest resolves the current user and its group name so tests
// can chown to self unprivileged. Skips when either lookup fails.
func currentOwnerForTest(t *testing.T) (uname, gidStr, gname string) {
	t.Helper()
	curr, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	g, err := user.LookupGroupId(curr.Gid)
	if err != nil {
		t.Skipf("current gid %s has no group name: %v", curr.Gid, err)
	}
	return curr.Username, curr.Gid, g.Name
}

// supplementaryGroupForTest returns a group the current user belongs to whose
// gid differs from the primary gid, so chown assertions cannot be satisfied
// by MkdirAll/process defaults (the fresh dir already carries the primary
// gid). Skips the test when the user has no supplementary group.
func supplementaryGroupForTest(t *testing.T) (gidStr, gname string) {
	t.Helper()
	curr, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	gids, err := curr.GroupIds()
	if err != nil {
		t.Skipf("cannot list group memberships: %v", err)
	}
	for _, gid := range gids {
		if gid == curr.Gid {
			continue
		}
		g, err := user.LookupGroupId(gid)
		if err != nil {
			continue
		}
		return gid, g.Name
	}
	t.Skip("current user has no supplementary group for a strong chown assertion")
	return "", ""
}

// TestPresentDirectoryOwnerGroupApplied pins that WithOwner and WithGroup
// TestPresentDirectoryOwnerGroupApplied pins that WithOwner and WithGroup
// (group by name, exercising the os/user.LookupGroup fallback) are applied to
// the managed directory. Chowning to the current user's own uid/gid works
// without privileges.
func TestPresentDirectoryOwnerGroupApplied(t *testing.T) {
	resource.ResetRepository()
	uname, gidStr, gname := currentOwnerForTest(t)
	u, err := user.Lookup(uname)
	if err != nil {
		t.Fatalf("lookup %s: %v", uname, err)
	}
	wantUID, err := strconv.Atoi(u.Uid)
	if err != nil {
		t.Fatalf("parse uid %s: %v", u.Uid, err)
	}
	wantGID, err := strconv.Atoi(gidStr)
	if err != nil {
		t.Fatalf("parse gid %s: %v", gidStr, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "owneddir")

	Present(path, WithOwner(uname), WithGroup(gname))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	gotUID, gotGID := dirUIDGid(t, path)
	if gotUID != wantUID {
		t.Errorf("owner %s applied uid %d, want %d", uname, gotUID, wantUID)
	}
	if gotGID != wantGID {
		t.Errorf("group %s applied gid %d, want %d", gname, gotGID, wantGID)
	}
}

// TestDirUnknownGroupFails pins the error path of dir's group resolution: an
// unresolvable group name must fail the apply loudly.
func TestDirUnknownGroupFails(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "badgroupdir")
	err := Ensure(path, WithGroup("gonf-no-such-group-8f3a"))
	if err == nil || !strings.Contains(err.Error(), "failed to resolve group") {
		t.Fatalf("expected group resolution error, got %v", err)
	}
}

func TestPresentDirectoryCreate(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "newdir")

	Present(path)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Errorf("expected %s to be a directory", path)
	}
}

func TestPresentDirectoryIdempotentWithMode(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "modedir")
	mode := os.FileMode(0o700)

	Present(path, WithMode(mode))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("expected mode %v, got %v", mode, info.Mode().Perm())
	}

	// Idempotency check
	if err := testapply.Apply(); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
}

func TestPresentDirectoryFailsWhenFileExists(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "myfile")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path)
	if err := testapply.Apply(); err == nil {
		t.Error("expected Apply to fail when a file exists at the directory path")
	}
}

func TestPresentAbsentNonEmptyDirWithoutPruneFails(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "nonempty")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, IsAbsent)
	if err := testapply.Apply(); err == nil {
		t.Error("expected Apply to fail when removing non-empty directory without prune")
	}
}

func TestPresentAbsentPruneDirectoryRecursive(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "pruneme")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, IsAbsent, WithPrune)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed recursively", path)
	}
}

func TestAbsentPruneDirectoryRecursive(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "pruneme2")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	Absent(path, WithPrune)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed recursively", path)
	}
}

func TestPresentDirectoryWithSource(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f1"), []byte("content1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "subdir", "f2"), []byte("content2"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("Create", func(t *testing.T) {
		resource.ResetRepository()
		Present(dst, WithSource(src))
		if err := testapply.Apply(); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}

		if _, err := os.Stat(filepath.Join(dst, "f1")); err != nil {
			t.Errorf("missing file f1: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "subdir", "f2")); err != nil {
			t.Errorf("missing file f2: %v", err)
		}
	})

	t.Run("Prune", func(t *testing.T) {
		resource.ResetRepository()
		// Add extra file to dst
		extra := filepath.Join(dst, "extra")
		if err := os.WriteFile(extra, []byte("extra"), 0o644); err != nil {
			t.Fatal(err)
		}

		Present(dst, WithSource(src), WithPrune)
		if err := testapply.Apply(); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}

		if _, err := os.Stat(extra); !os.IsNotExist(err) {
			t.Errorf("expected %s to be pruned", extra)
		}
	})
}

func TestSourceCopyUsesFileModeDefaultNotDirMode(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(src, "f1")
	if err := os.WriteFile(f1, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use non-default dir mode to prove files don't use it
	Present(dst, WithSource(src), WithMode(0o700))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "f1"))
	if err != nil {
		t.Fatal(err)
	}
	// Default fileMode is 0o640
	if info.Mode().Perm() != 0o640 {
		t.Errorf("expected file mode 0o640, got %v", info.Mode().Perm())
	}
}

func TestSourceCopyRespectsExplicitWithFileMode(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f1"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	explicitMode := os.FileMode(0o600)
	Present(dst, WithSource(src), WithFileMode(explicitMode))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "f1"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != explicitMode {
		t.Errorf("expected file mode %v, got %v", explicitMode, info.Mode().Perm())
	}
}

func TestSourceCopyStripsTmplSuffixOnCopiedFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "foo.conf.tmpl"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSource(src))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "foo.conf")); err != nil {
		t.Errorf("expected stripped file foo.conf to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "foo.conf.tmpl")); !os.IsNotExist(err) {
		t.Errorf("expected non-stripped file foo.conf.tmpl to NOT exist")
	}
}

func TestSourceCopyWithPruneKeepsTemplatedFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "foo.conf.tmpl"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First apply to create the file
	Present(dst, WithSource(src))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Now apply with prune
	resource.ResetRepository()
	Present(dst, WithSource(src), WithPrune)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "foo.conf")); err != nil {
		t.Errorf("expected foo.conf to be kept during pruning: %v", err)
	}
}

func TestSourceCopyParamMatchesSingleFilePath(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(src, "foo.conf.tmpl")
	if err := os.WriteFile(f1, []byte("path is {{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSource(src))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "foo.conf"))
	if err != nil {
		t.Fatal(err)
	}
	expected := "path is " + f1
	if string(got) != expected {
		t.Errorf("expected %q, got %q", expected, string(got))
	}
}

func TestSourceCopyRecreatesSymlinkNotContent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(src, "realfile")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(src, "link")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSource(src))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	dstLink := filepath.Join(dst, "link")
	info, err := os.Lstat(dstLink)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %s to be a symlink", dstLink)
	}
}

func TestSourceGlobInstallsMatchingFiles(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.rb", "b.rb", "skip.txt"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	Present(dst, WithSourceGlob(filepath.Join(src, "*.rb")), WithFileMode(0o640))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, name := range []string{"a.rb", "b.rb"} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if string(got) != name {
			t.Fatalf("%s content = %q", name, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "skip.txt")); !os.IsNotExist(err) {
		t.Fatal("skip.txt should not have been installed")
	}

	// Idempotent
	resource.ResetRepository()
	Present(dst, WithSourceGlob(filepath.Join(src, "*.rb")), WithFileMode(0o640))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
}

func TestSourceGlobPrune(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.rb"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "keep.rb"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "old.rb"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dst, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSourceGlob(filepath.Join(src, "*.rb")), WithPrune)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "old.rb")); !os.IsNotExist(err) {
		t.Fatal("old.rb should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.rb")); err != nil {
		t.Fatal("keep.rb should remain")
	}
	if _, err := os.Stat(filepath.Join(dst, "subdir")); err != nil {
		t.Fatal("subdir should not be pruned")
	}
}

// TestSourceGlobPruneKeepSetMatchesCountingMatches pins the lockstep
// between copySourceGlob and pruneGlob: both classify matches through the
// one shared predicate (GlobMatchCounts), so the prune keep-set is exactly
// the basenames copySourceGlob would copy. A destination regular file whose
// name matches only a non-counting source entry (here: the dangling
// symlink "ghost" and the unmatched "stale.rb") is converged away instead
// of being kept forever, while a counting symlink-to-file match keeps its
// basename.
func TestSourceGlobPruneKeepSetMatchesCountingMatches(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.rb"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("keep.rb", filepath.Join(src, "tofile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(src, "ghost")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep.rb", "tofile", "ghost", "stale.rb"} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dst, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Cross-consistency (via the shared predicate, not the fs): the
	// counting-match set is the same set copySourceGlob copies and
	// pruneGlob keeps.
	matches, err := filepath.Glob(filepath.Join(src, "*"))
	if err != nil {
		t.Fatal(err)
	}
	wantKeep := map[string]bool{}
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			t.Fatal(err)
		}
		if GlobMatchCounts(match, info) {
			wantKeep[filepath.Base(match)] = true
		}
	}
	if !wantKeep["keep.rb"] || !wantKeep["tofile"] || len(wantKeep) != 2 {
		t.Fatalf("counting matches = %v, want exactly {keep.rb, tofile}", wantKeep)
	}

	Present(dst, WithSourceGlob(filepath.Join(src, "*")), WithPrune)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, name := range []string{"keep.rb", "tofile"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Fatalf("%s should remain (counting match): %v", name, err)
		}
	}
	for _, name := range []string{"ghost", "stale.rb"} {
		if _, err := os.Stat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been pruned (non-counting match)", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "subdir")); err != nil {
		t.Fatal("subdir should not be pruned")
	}
}

// TestSourceTreePruneDryRunKeepsStaleFiles guards against data loss: a
// dry-run apply (gonf -n) of a Dir with WithSource+WithPrune must only
// preview the prune (StatusWouldChange note), never delete stale destination
// files.
func TestSourceTreePruneDryRunKeepsStaleFiles(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dst, "stale.txt")
	staleContent := "precious stale content"
	if err := os.WriteFile(stale, []byte(staleContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale directory too, so the dry-run's note id for stale DIRECTORIES
	// (deliberately File[<path>], same as the real run) stays pinned.
	staleDir := filepath.Join(dst, "staleDir")
	if err := os.Mkdir(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}

	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	Present(dst, WithSource(src), WithPrune)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.ReadFile(stale)
	if err != nil {
		t.Fatalf("dry-run prune deleted %s: %v", stale, err)
	}
	if string(got) != staleContent {
		t.Errorf("stale.txt content = %q, want %q", got, staleContent)
	}
	gotKeep, err := os.ReadFile(filepath.Join(dst, "keep.txt"))
	if err != nil {
		t.Fatalf("keep.txt should remain after dry-run prune: %v", err)
	}
	if string(gotKeep) != "keep" {
		t.Errorf("keep.txt content = %q, want %q", gotKeep, "keep")
	}

	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	if !strings.Contains(buf.String(), "would-change File["+stale+"]") {
		t.Errorf("expected a would-change note for %s, summary:\n%s", stale, buf.String())
	}
	// Pin the dry-run id for stale directories: it must stay File[<path>]
	// (not Directory[<path>]) so it matches the real-run note id.
	if !strings.Contains(buf.String(), "would-change File["+staleDir+"]") {
		t.Errorf("expected a would-change note with the File[...] id for the stale directory %s, summary:\n%s", staleDir, buf.String())
	}
}

// TestSourceTreeDryRunCreatesNothing guards the dry-run contract for a Dir
// with WithSource: gonf -n must not mutate the filesystem — no destination
// directories are created (no MkdirAll), no files are copied, no symlinks
// are written, no attributes are applied. The preview only records
// StatusWouldChange notes, per entry, mirroring the real run's per-file and
// per-symlink note flow (copySourceFile → file.Ensure and
// copySourceSymlink → link.Ensure already self-guard; copySourceDir is
// gated here).
func TestSourceTreeDryRunCreatesNothing(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f1"), []byte("content1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "f2"), []byte("content2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An absolute link target: relative targets inside a source tree are not
	// remapped into the destination (see copySourceSymlink's docstring), so
	// the dangling-link refusal would reject a relative target here.
	realfile := filepath.Join(src, "realfile")
	if err := os.WriteFile(realfile, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realfile, filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	Present(dst, WithSource(src))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("dry-run Apply failed: %v", err)
	}

	// Nothing may exist on disk: no MkdirAll happened anywhere in the tree.
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("dry-run created destination %s: %v", dst, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "sub")); !os.IsNotExist(err) {
		t.Errorf("dry-run created destination subdir: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "f1")); !os.IsNotExist(err) {
		t.Errorf("dry-run copied file f1: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "link")); !os.IsNotExist(err) {
		t.Errorf("dry-run created symlink link: %v", err)
	}

	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	summary := buf.String()
	for _, want := range []string{
		"would-change Directory[" + dst + "]",
		"would-change Directory[" + filepath.Join(dst, "sub") + "]",
		"would-change File[" + filepath.Join(dst, "f1") + "]",
		"would-change File[" + filepath.Join(dst, "sub", "f2") + "]",
		"would-change Symlink[" + filepath.Join(dst, "link") + "]",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("expected %q in dry-run summary, summary:\n%s", want, summary)
		}
	}
}

// TestSourceGlobDryRunCopiesNothing pins the glob half of the dry-run
// contract: a dry-run apply of a Dir with WithSourceGlob copies no files and
// creates no destination directory; it only records would-change notes.
func TestSourceGlobDryRunCopiesNothing(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.rb", "b.rb"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	Present(dst, WithSourceGlob(filepath.Join(src, "*.rb")))
	if err := testapply.Apply(); err != nil {
		t.Fatalf("dry-run Apply failed: %v", err)
	}

	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("dry-run created destination %s: %v", dst, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "a.rb")); !os.IsNotExist(err) {
		t.Errorf("dry-run copied a.rb: %v", err)
	}

	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	summary := buf.String()
	for _, want := range []string{
		"would-change File[" + filepath.Join(dst, "a.rb") + "]",
		"would-change File[" + filepath.Join(dst, "b.rb") + "]",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("expected %q in dry-run summary, summary:\n%s", want, summary)
		}
	}
}

// TestSourceDryRunPruneWithNonexistentDestIsClean keeps a fresh dry-run of a
// sync+prune working end to end: the destination is never created under -n,
// so the prune walk/read must preview nothing (not fail on the missing
// root) and must not mutate anything. The real path cannot reach this state
// — ensureDirectorySelf creates the destination before pruning runs — so the
// guard is dry-run-only and the non-dry-run behavior is untouched.
func TestSourceDryRunPruneWithNonexistentDestIsClean(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f1"), []byte("f1"), 0o644); err != nil {
		t.Fatal(err)
	}

	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	t.Run("Tree", func(t *testing.T) {
		resource.ResetRepository()
		dst := filepath.Join(dir, "dst-tree")
		if err := Ensure(dst, WithSource(src), WithPrune); err != nil {
			t.Fatalf("dry-run tree Apply failed: %v", err)
		}
		if _, err := os.Lstat(dst); !os.IsNotExist(err) {
			t.Errorf("dry-run created destination %s: %v", dst, err)
		}
	})

	t.Run("Glob", func(t *testing.T) {
		resource.ResetRepository()
		dst := filepath.Join(dir, "dst-glob")
		if err := Ensure(dst, WithSourceGlob(filepath.Join(src, "*.rb")), WithPrune); err != nil {
			t.Fatalf("dry-run glob Apply failed: %v", err)
		}
		if _, err := os.Lstat(dst); !os.IsNotExist(err) {
			t.Errorf("dry-run created destination %s: %v", dst, err)
		}
	})
}

// noteLinesOf turns PrintSummary's output into a comparable form: the
// summary header becomes a single "counts ..." pseudo-line (would-change
// folded into changed) and each per-note line keeps its "status id" shape
// with would-change folded to changed, sorted. dryPath is rewritten to
// realPath (no-op when equal), so the same scenario run in both modes
// yields identical output despite the two runs' differing destination paths
// and status vocabulary. The folded counts make the comparison sensitive to
// ok notes too, which PrintSummary never lists as lines.
func noteLinesOf(t *testing.T, summary, dryPath, realPath string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "summary:") {
			var ok, changed, skipped, would int
			if _, err := fmt.Sscanf(line, "summary: %d ok, %d changed, %d skipped, %d would-change", &ok, &changed, &skipped, &would); err != nil {
				t.Fatalf("unparsable summary header %q: %v", line, err)
			}
			lines = append(lines, fmt.Sprintf("counts ok=%d changed=%d skipped=%d", ok, changed+would, skipped))
			continue
		}
		if dryPath != realPath {
			line = strings.ReplaceAll(line, dryPath, realPath)
		}
		line = strings.ReplaceAll(line, "would-change ", "changed ")
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

// summaryOf applies and returns the raw PrintSummary output.
func summaryOf(t *testing.T, apply func() error) string {
	t.Helper()
	if err := apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	return buf.String()
}

// TestSourceTreePruneNotesChanged pins the real-run visibility of tree
// prunes (u12): every removed path — a stale FILE and a stale DIRECTORY
// alike — must be noted as File[<path>] StatusChanged after its removal
// (mirroring pruneGlob), so the summary counts them and so that
// AnyChanged("Directory[<dst>]") — the daemon-reload watch id — gates on
// tree prunes through the File[<dirpath>/...] note convention. The stale
// directory deliberately keeps the File[<path>] id the dry-run uses for the
// same path, keeping the dry-run and real-run note sets identical.
func TestSourceTreePruneNotesChanged(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(dst, "stale.txt")
	if err := os.WriteFile(staleFile, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleDir := filepath.Join(dst, "staleDir")
	if err := os.Mkdir(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "inner.txt"), []byte("inner"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(dst, WithSource(src), WithPrune)
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Both pruned paths are really gone (RemoveAll covers the stale
	// directory's contents).
	if _, err := os.Lstat(staleFile); !os.IsNotExist(err) {
		t.Errorf("expected %s to be pruned", staleFile)
	}
	if _, err := os.Lstat(staleDir); !os.IsNotExist(err) {
		t.Errorf("expected %s to be pruned recursively", staleDir)
	}

	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	summary := buf.String()
	for _, want := range []string{
		"changed File[" + staleFile + "]",
		"changed File[" + staleDir + "]",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("expected %q in summary, summary:\n%s", want, summary)
		}
	}
	kept := filepath.Join(dst, "keep.txt")
	if strings.Contains(summary, "changed File["+kept+"]") {
		t.Errorf("kept file %s must not be noted changed, summary:\n%s", kept, summary)
	}

	// Daemon-reload gating: the Directory[<dst>] watch id must see the tree
	// prunes via the File[<dst>/...] note convention.
	if !resource.AnyChanged("Directory[" + dst + "]") {
		t.Errorf("AnyChanged(Directory[%s]) must be true after tree prunes, summary:\n%s", dst, summary)
	}
}

// TestSourceTreeSubdirNotesReal pins copySourceDir's note flow in the real
// run (mirroring ensureDirectorySelf's root-dir flow): a fresh destination
// notes Directory[<dst/sub>] changed alongside the copied file notes, and a
// second run notes Directory[<dst/sub>] ok — attributes are only
// re-enforced (converged) — with no changed notes left for the subdirs.
func TestSourceTreeSubdirNotesReal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	sub := filepath.Join(dst, "sub")

	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f1"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "f2"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("Fresh", func(t *testing.T) {
		resource.ResetRepository()
		summary := summaryOf(t, func() error {
			Present(dst, WithSource(src))
			return testapply.Apply()
		})
		for _, want := range []string{
			"changed Directory[" + dst + "]",
			"changed Directory[" + sub + "]",
			"changed File[" + filepath.Join(dst, "f1") + "]",
			"changed File[" + filepath.Join(sub, "f2") + "]",
		} {
			if !strings.Contains(summary, want) {
				t.Errorf("expected %q in summary, summary:\n%s", want, summary)
			}
		}
	})

	t.Run("Idempotent", func(t *testing.T) {
		resource.ResetRepository()
		summary := summaryOf(t, func() error {
			Present(dst, WithSource(src))
			return testapply.Apply()
		})
		// PrintSummary lists only non-ok notes, so the Directory[<sub>] ok
		// note (and the files') is pinned via the count: root dir, f1, sub
		// and f2 are exactly 4 ok — without the subdir ok note only 3 would
		// be counted. The absence assertions below pin no-changed for the
		// subdirs.
		const wantHeader = "summary: 4 ok, 0 changed, 0 skipped, 0 would-change"
		if !strings.Contains(summary, wantHeader) {
			t.Errorf("expected %q in summary, summary:\n%s", wantHeader, summary)
		}
		for _, unwanted := range []string{
			"changed Directory[" + sub + "]",
			"changed File[" + filepath.Join(sub, "f2") + "]",
		} {
			if strings.Contains(summary, unwanted) {
				t.Errorf("second run must not contain %q, summary:\n%s", unwanted, summary)
			}
		}
	})
}

// TestSourceTreeDryRunParity pins that dry-run and real-run note sets are
// identical for source-tree flows: for the same scenario the dry-run's note
// set must equal the real run's once "would-change" statuses are folded to
// "changed" and the dry destination path is rewritten to the real one (note
// ids carry full paths, which differ between the two runs otherwise).
// Fresh covers creation (would-change ↔ changed, including the new
// copySourceDir subdir note); Existing covers converged destinations
// (ok ↔ ok, including the new existing-dir ok note) plus prune
// (would-change ↔ changed).
func TestSourceTreeDryRunParity(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f1"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "f2"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}

	runReal := func(t *testing.T, dst string, opts ...DirOption) string {
		t.Helper()
		resource.ResetRepository()
		Present(dst, append([]DirOption{WithSource(src)}, opts...)...)
		var applyErr error
		summary := summaryOf(t, func() error {
			applyErr = testapply.Apply()
			return applyErr
		})
		if applyErr != nil {
			t.Fatalf("real Apply failed: %v", applyErr)
		}
		return summary
	}

	runDry := func(t *testing.T, dst string, opts ...DirOption) string {
		t.Helper()
		resource.ResetRepository()
		resource.SetDryRun(true)
		t.Cleanup(func() { resource.SetDryRun(false) })
		Present(dst, append([]DirOption{WithSource(src)}, opts...)...)
		var applyErr error
		summary := summaryOf(t, func() error {
			applyErr = testapply.Apply()
			return applyErr
		})
		resource.SetDryRun(false)
		if applyErr != nil {
			t.Fatalf("dry-run Apply failed: %v", applyErr)
		}
		return summary
	}

	assertParity := func(t *testing.T, realSummary, drySummary, realDst, dryDst string) {
		t.Helper()
		want := noteLinesOf(t, realSummary, realDst, realDst)
		got := noteLinesOf(t, drySummary, dryDst, realDst)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("dry-run note set differs from real run:\nreal: %v\ndry:  %v", want, got)
		}
	}

	t.Run("Fresh", func(t *testing.T) {
		realDst := filepath.Join(dir, "parity-real-fresh")
		dryDst := filepath.Join(dir, "parity-dry-fresh")
		realSummary := runReal(t, realDst)
		drySummary := runDry(t, dryDst)
		assertParity(t, realSummary, drySummary, realDst, dryDst)

		// Spot-check the ids the parity comparison hinges on: the subdir
		// creation is noted in both modes (would-change vs changed).
		for _, want := range []string{
			"changed Directory[" + filepath.Join(realDst, "sub") + "]",
			"changed File[" + filepath.Join(realDst, "sub", "f2") + "]",
		} {
			if !strings.Contains(realSummary, want) {
				t.Errorf("expected %q in real summary, summary:\n%s", want, realSummary)
			}
		}
	})

	t.Run("Existing", func(t *testing.T) {
		realDst := filepath.Join(dir, "parity-real-existing")
		dryDst := filepath.Join(dir, "parity-dry-existing")

		// Build the real destination with an unobserved first apply, then
		// add a stale file so the observed run has something to prune.
		runReal(t, realDst)
		if err := os.WriteFile(filepath.Join(realDst, "stale.txt"), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
		realSummary := runReal(t, realDst, WithPrune)

		// The dry run must not create anything, so its destination is
		// populated by hand to the same starting state (same file contents,
		// same stale file).
		if err := os.MkdirAll(filepath.Join(dryDst, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dryDst, "f1"), []byte("one"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dryDst, "sub", "f2"), []byte("two"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dryDst, "stale.txt"), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
		drySummary := runDry(t, dryDst, WithPrune)

		assertParity(t, realSummary, drySummary, realDst, dryDst)

		// Spot-check the new converged subdir note in the real run:
		// PrintSummary lists only non-ok notes, so the ok note is proven via
		// the count — root dir, f1, sub and f2 are 4 ok, the pruned stale
		// file is the single changed note.
		const wantHeader = "summary: 4 ok, 1 changed, 0 skipped, 0 would-change"
		if !strings.Contains(realSummary, wantHeader) {
			t.Errorf("expected %q in real summary, summary:\n%s", wantHeader, realSummary)
		}
	})
}

// TestSourceTreeRelativeSymlinkDryRunParity pins dry-run/real-run parity
// for source-tree symlinks with RELATIVE targets pointing at other
// source-tree entries (task 122). A real run succeeds because the walk
// materializes the destination-side target (a sibling) before link.Ensure's
// target-exists assert runs on the later-sorted link; the dry run does not
// materialize the destination (y02), so copySourceSymlink validates the
// relative target against the SOURCE tree instead and mirrors link.Ensure's
// note flow — both modes must end up with identical (folded) note sets, on
// a fresh and on a converged destination, and the raw target string must
// survive on disk. A relative link dangling in the SOURCE tree too keeps
// link.Ensure's documented refusal in BOTH modes.
func TestSourceTreeRelativeSymlinkDryRunParity(t *testing.T) {
	buildSrc := func(t *testing.T) string {
		t.Helper()
		src := filepath.Join(t.TempDir(), "src")
		if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "sub", "f2"), []byte("two"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "f1"), []byte("one"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "realfile"), []byte("hi"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Relative in-tree targets; both sort BEFORE their links, so a real
		// run has materialized the destination-side target by the time the
		// link is validated (the pre-existing walk-order limitation for
		// targets sorting later is out of scope here).
		if err := os.Symlink("sub", filepath.Join(src, "zdirlink")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("realfile", filepath.Join(src, "zfilelink")); err != nil {
			t.Fatal(err)
		}
		return src
	}

	assertOnDiskRawTargets := func(t *testing.T, dst string) {
		t.Helper()
		for name, want := range map[string]string{
			"zdirlink":  "sub",
			"zfilelink": "realfile",
		} {
			linkPath := filepath.Join(dst, name)
			info, err := os.Lstat(linkPath)
			if err != nil {
				t.Fatalf("missing symlink %s: %v", linkPath, err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("%s is not a symlink", linkPath)
				continue
			}
			got, err := os.Readlink(linkPath)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("symlink %s target = %q, want the preserved raw target %q", linkPath, got, want)
			}
		}
	}

	t.Run("Real", func(t *testing.T) {
		resource.ResetRepository()
		src := buildSrc(t)
		dst := filepath.Join(t.TempDir(), "dst")
		summary := summaryOf(t, func() error {
			Present(dst, WithSource(src))
			return testapply.Apply()
		})
		if _, err := os.Stat(filepath.Join(dst, "sub", "f2")); err != nil {
			t.Errorf("missing copied file: %v", err)
		}
		assertOnDiskRawTargets(t, dst)
		for _, want := range []string{
			"changed Symlink[" + filepath.Join(dst, "zdirlink") + "]",
			"changed Symlink[" + filepath.Join(dst, "zfilelink") + "]",
		} {
			if !strings.Contains(summary, want) {
				t.Errorf("expected %q in summary, summary:\n%s", want, summary)
			}
		}
	})

	t.Run("DryRunFresh", func(t *testing.T) {
		resource.ResetRepository()
		src := buildSrc(t)
		dst := filepath.Join(t.TempDir(), "dst")
		resource.SetDryRun(true)
		t.Cleanup(func() { resource.SetDryRun(false) })
		summary := summaryOf(t, func() error {
			Present(dst, WithSource(src))
			return testapply.Apply()
		})
		// Nothing on disk, and NO loud refusal: pre-fix, the relative
		// in-tree targets failed here with 'refusing broken link to'.
		if _, err := os.Lstat(dst); !os.IsNotExist(err) {
			t.Errorf("dry-run created destination %s: %v", dst, err)
		}
		for _, want := range []string{
			"would-change Symlink[" + filepath.Join(dst, "zdirlink") + "]",
			"would-change Symlink[" + filepath.Join(dst, "zfilelink") + "]",
		} {
			if !strings.Contains(summary, want) {
				t.Errorf("expected %q in dry-run summary, summary:\n%s", want, summary)
			}
		}
	})

	// The same scenario run in both modes must produce identical folded
	// note sets, fresh and converged (would-change folded to changed, dry
	// path rewritten to the real one by noteLinesOf).
	t.Run("Parity", func(t *testing.T) {
		src := buildSrc(t)
		realDst := filepath.Join(t.TempDir(), "real")
		dryDst := filepath.Join(t.TempDir(), "dry")

		realApply := func(t *testing.T) string {
			t.Helper()
			resource.ResetRepository()
			return summaryOf(t, func() error {
				Present(realDst, WithSource(src))
				return testapply.Apply()
			})
		}
		dryApply := func(t *testing.T) string {
			t.Helper()
			resource.ResetRepository()
			resource.SetDryRun(true)
			defer resource.SetDryRun(false)
			return summaryOf(t, func() error {
				Present(dryDst, WithSource(src))
				return testapply.Apply()
			})
		}
		assertParity := func(t *testing.T, realSummary, drySummary string) {
			t.Helper()
			want := noteLinesOf(t, realSummary, realDst, realDst)
			got := noteLinesOf(t, drySummary, dryDst, realDst)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("dry-run note set differs from real run:\nreal: %v\ndry:  %v", want, got)
			}
		}

		realSummary := realApply(t)
		drySummary := dryApply(t)
		assertParity(t, realSummary, drySummary)
		assertOnDiskRawTargets(t, realDst)

		// Converged: the destination trees are already in place (the dry
		// destination built by hand — dry-run materializes nothing), so both
		// modes must note everything ok, symlinks included.
		_ = realApply(t)
		if err := os.MkdirAll(filepath.Join(dryDst, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			"f1":       "one",
			"realfile": "hi",
			"sub/f2":   "two",
		} {
			if err := os.WriteFile(filepath.Join(dryDst, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink("sub", filepath.Join(dryDst, "zdirlink")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("realfile", filepath.Join(dryDst, "zfilelink")); err != nil {
			t.Fatal(err)
		}
		realSummary = realApply(t)
		drySummary = dryApply(t)
		assertParity(t, realSummary, drySummary)
		const wantHeader = "summary: 7 ok, 0 changed, 0 skipped, 0 would-change"
		if !strings.Contains(realSummary, wantHeader) {
			t.Errorf("expected %q in converged real summary, summary:\n%s", wantHeader, realSummary)
		}
	})

	// A relative link dangling in the SOURCE tree too has no source-side
	// counterpart, so both modes keep link.Ensure's documented broken-link
	// refusal: the destination-side target is never materialized in either
	// mode (nothing copies a target with no source entry).
	t.Run("DanglingRefusesInBothModes", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			dry  bool
		}{
			{"Real", false},
			{"DryRun", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resource.ResetRepository()
				if tc.dry {
					resource.SetDryRun(true)
					t.Cleanup(func() { resource.SetDryRun(false) })
				}
				src := filepath.Join(t.TempDir(), "src")
				if err := os.Mkdir(src, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(src, "good"), []byte("good"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing", filepath.Join(src, "dangling")); err != nil {
					t.Fatal(err)
				}
				dst := filepath.Join(t.TempDir(), "dst")
				Present(dst, WithSource(src))
				err := testapply.Apply()
				if err == nil || !strings.Contains(err.Error(), "refusing broken link to") {
					t.Fatalf("expected the broken-link refusal in %s mode, got: %v", tc.name, err)
				}
				if tc.dry {
					if _, err := os.Lstat(dst); !os.IsNotExist(err) {
						t.Errorf("dry-run created destination %s: %v", dst, err)
					}
				}
			})
		}
	})
}

// TestAbsentDoesNotMutateCallerOptionSlice guards against 100 Go Mistakes
// #25: Absent used to append IsAbsent onto the caller-owned variadic slice,
// writing into the spare capacity of a reusable option list and silently
// turning later Present calls built from the same backing array into
// deletions.
func TestAbsentDoesNotMutateCallerOptionSlice(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")
	keep := filepath.Join(dir, "keep")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}

	// Reusable option list with spare capacity (len 2, cap 8). Sub-slices
	// share its backing array.
	base := make([]DirOption, 2, 8)
	base[0] = WithMode(0o755)
	base[1] = WithPrune
	before := make([]DirOption, len(base))
	copy(before, base)

	Absent(gone, base[:1]...)
	Present(keep, base[:2]...)

	// (a) The caller-owned backing array must be unchanged.
	for i := range base {
		if reflect.ValueOf(base[i]).Pointer() != reflect.ValueOf(before[i]).Pointer() {
			t.Fatalf("caller option slice mutated at index %d: IsAbsent was injected into the caller's backing array", i)
		}
	}

	// (b) The Present resource built from the same backing array must not
	// have become absent.
	if err := testapply.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	info, err := os.Stat(keep)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected %s to still exist (Present must not be absent): %v", keep, err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", gone)
	}
}

// TestDirEnsureRefusesSymlinkAtTarget pins the dir resource's symlink rule:
// a symlink at the final target path component is never followed. A planted
// symlink-to-dir (or symlink-to-file) must fail loudly with the victim —
// the symlink's destination outside the managed tree — left untouched and
// the symlink itself surviving at the target path.
func TestDirEnsureRefusesSymlinkAtTarget(t *testing.T) {
	tests := map[string]struct {
		makeVictim     func(t *testing.T, path string)
		wantVictimPerm os.FileMode
	}{
		// The victim's mode deliberately differs from the requested mode
		// (0o700 below): a chmod followed through the link would be visible,
		// so the victim-perm assertion is not tautological (same pattern as
		// the file package's b12 tests).
		"symlink to dir": {
			makeVictim: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o750); err != nil {
					t.Fatal(err)
				}
			},
			wantVictimPerm: 0o750,
		},
		"symlink to file": {
			makeVictim: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("victim data"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantVictimPerm: 0o600,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			resource.ResetRepository()
			base := t.TempDir()
			victim := filepath.Join(base, "victim")
			tt.makeVictim(t, victim)
			target := filepath.Join(base, "managed")
			if err := os.Symlink(victim, target); err != nil {
				t.Fatal(err)
			}

			uname, _, gname := currentOwnerForTest(t)
			err := Ensure(target, WithMode(0o700), WithOwner(uname), WithGroup(gname))
			if err == nil {
				t.Fatal("expected Ensure to refuse a symlink at the target path")
			}
			if !strings.Contains(err.Error(), target) {
				t.Errorf("error should name the target path: %v", err)
			}
			// Pin the DEDICATED symlink refusal, not the generic non-dir error:
			// the ensure-level Lstat check must fire, not just IsDir logic.
			if !strings.Contains(err.Error(), "is a symlink") {
				t.Errorf("error should be the explicit symlink refusal: %v", err)
			}

			// The victim outside the managed tree stays untouched.
			info, err := os.Stat(victim)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tt.wantVictimPerm {
				t.Errorf("victim perms changed: got %v, want %v", info.Mode().Perm(), tt.wantVictimPerm)
			}

			// The planted symlink itself survives.
			linkInfo, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if linkInfo.Mode()&os.ModeSymlink == 0 {
				t.Errorf("expected %s to still be a symlink", target)
			}
		})
	}
}

// TestDirApplyAttributesToRefusesSymlinkAtTarget is the direct unit-level
// pin, mirroring the file package's test of the same name: applyAttributesTo
// must open the target with O_NOFOLLOW|O_DIRECTORY so the kernel refuses a
// symlink planted at the path instead of following it.
func TestDirApplyAttributesToRefusesSymlinkAtTarget(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "victim-dir")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "planted")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}

	// Empty user/group: uid/gid stay -1 (no-op chown), mirroring file's test.
	err := applyAttributesTo(target, 0o750, "", "")
	if err == nil {
		t.Fatal("expected applyAttributesTo to refuse a symlink at the target")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("error should mention the target path: %v", err)
	}

	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("victim perms changed: got %v, want 0o700", info.Mode().Perm())
	}
}

// TestDirApplyAttributesToRefusesNonDirectory pins the O_DIRECTORY half of
// the guard: a plain file at the target path is refused too (ENOTDIR), not
// chmodded as if it were a directory.
func TestDirApplyAttributesToRefusesNonDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "plainfile")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := applyAttributesTo(target, 0o750, "", "")
	if err == nil {
		t.Fatal("expected applyAttributesTo to refuse a non-directory at the target")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("error should mention the target path: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file perms changed: got %v, want 0o600", info.Mode().Perm())
	}
}

// TestApplyAttributesToFallbackOnOwnerUnreadable pins the path-based
// fallback for owner-unreadable modes, mirroring the file package's test of
// the same name: POSIX denies the owner O_RDONLY on a directory whose mode
// lacks owner-read (e.g. 0o300, write+execute only), so a non-root run
// cannot open its own directory for the fd-based attribute application;
// applyAttributesTo must fall back to path-based chown/chmod (which do not
// need open access) instead of erroring with a spurious EACCES. Skipped as
// root: CAP_DAC_OVERRIDE lets root's O_RDONLY|O_DIRECTORY open succeed, so
// the fallback never triggers there (and cannot be exercised).
func TestApplyAttributesToFallbackOnOwnerUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("fallback only triggers for non-root: root's CAP_DAC_OVERRIDE lets the O_NOFOLLOW open succeed")
	}
	resource.ResetRepository()
	base := t.TempDir()
	path := filepath.Join(base, "unreadable")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	// 0o300 keeps owner-write+execute but strips owner-read, so the
	// O_RDONLY|O_DIRECTORY open fails for non-root while chown/chmod as the
	// owner still work.
	if err := os.Chmod(path, 0o300); err != nil {
		t.Fatal(err)
	}

	// Empty user/group: uid/gid stay -1 (no-op chown), mirroring file's test.
	if err := applyAttributesTo(path, 0o750, "", ""); err != nil {
		t.Fatalf("applyAttributesTo failed on an owner-unreadable directory: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Errorf("target changed type: got %v", info.Mode())
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("mode not applied via fallback: got %v, want 0o750", got)
	}
}

// TestDirEnsureGroupByNameResolvesViaLookupGroup mirrors the file package's
// TestEnsureGroupByNameResolvesViaLookupGroup for directories: both
// WithGroup by name (exercising the os/user.LookupGroup fallback) and by
// numeric gid are applied to the managed directory. Chowning to the current
// user's own gid works without privileges.
func TestDirEnsureGroupByNameResolvesViaLookupGroup(t *testing.T) {
	resource.ResetRepository()
	// Assert against a SUPPLEMENTARY group (gid differing from the primary
	// gid): a freshly created dir already carries the primary gid, so a
	// silently skipped chown would be indistinguishable with the primary
	// group. A supplementary group requires an actual gid change to pass.
	gidStr, gname := supplementaryGroupForTest(t)
	wantGid, err := strconv.Atoi(gidStr)
	if err != nil {
		t.Fatalf("parse gid %s: %v", gidStr, err)
	}

	base := t.TempDir()
	byName := filepath.Join(base, "byname")
	if err := Ensure(byName, WithGroup(gname)); err != nil {
		t.Fatalf("Ensure with group name %s: %v", gname, err)
	}
	if _, got := dirUIDGid(t, byName); got != wantGid {
		t.Errorf("group name %s resolved to gid %d, want %d", gname, got, wantGid)
	}

	byNum := filepath.Join(base, "bynum")
	if err := Ensure(byNum, WithGroup(gidStr)); err != nil {
		t.Fatalf("Ensure with numeric gid %s: %v", gidStr, err)
	}
	if _, got := dirUIDGid(t, byNum); got != wantGid {
		t.Errorf("numeric gid %s applied as %d, want %d", gidStr, got, wantGid)
	}
}

// TestEnsureSourceAndSourceGlobConflict pins the build()-side validation:
// the conflicting option combination must surface as a RETURNED error (apply
// time, e.g. from plan apply) instead of exiting the process; Present keeps
// the fail-fast Fatal for record-time recipe misuse.
func TestEnsureSourceAndSourceGlobConflict(t *testing.T) {
	resource.ResetRepository()
	probe := t.TempDir()
	src := filepath.Join(probe, "src.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Ensure(filepath.Join(probe, "dst"), WithSource(src), WithSourceGlob(probe+"/*"))
	if err == nil {
		t.Fatal("expected WithSource + WithSourceGlob to be rejected with an error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should name the conflicting options, got: %v", err)
	}
}

// TestSourceGlobCopiesExactlyCountingMatches pins that copySourceGlob's
// copied set equals the shared predicate's counting set on a FRESH
// destination: a symlink-to-file is copied read-through, dangling links and
// dirs are skipped — so the copy side cannot diverge from GlobMatchCounts
// (task r12 review finding F1).
func TestSourceGlobCopiesExactlyCountingMatches(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.rb"), []byte("file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.rb", filepath.Join(dir, "tofile.rb")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "dangling.rb")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o700); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dst")
	if err := Ensure(dst, WithSourceGlob(filepath.Join(dir, "*"))); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	for _, name := range []string{"file.rb", "tofile.rb"} {
		info, err := os.Lstat(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("expected %s copied: %v", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s must be a regular file (read-through), got a symlink", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dst, "dangling.rb")); !os.IsNotExist(err) {
		t.Errorf("dangling.rb must be skipped, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "adir")); !os.IsNotExist(err) {
		t.Errorf("directory matches must be skipped, got %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "tofile.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "file\n" {
		t.Errorf("tofile.rb content = %q, want read-through %q", got, "file\n")
	}
}

// TestResolveGroupIDRejectsNegativeGid pins the negative-gid refusal (task
// 022), mirroring the file package's test.
func TestResolveGroupIDRejectsNegativeGid(t *testing.T) {
	if _, err := resolveGroupID("-1"); err == nil || !strings.Contains(err.Error(), "invalid gid -1") {
		t.Fatalf("expected an invalid-gid error, got %v", err)
	}
}

// TestEnsureRejectsNegativeGroup applies a directory with WithGroup("-1")
// and asserts a loud error instead of a silent no-op chown.
func TestEnsureRejectsNegativeGroup(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "dir")
	if err := Ensure(target, WithGroup("-1")); err == nil ||
		!strings.Contains(err.Error(), "invalid gid -1") {
		t.Fatalf("expected the negative-gid error, got %v", err)
	}
}

// TestSourceGlobPruneTemplatedMatchDoesNotFlap pins task 422: a glob match
// foo.tmpl installs as dst/foo (rendered, .tmpl stripped by the copy path);
// the prune keep-set must use the same written name, or dst/foo is pruned
// and re-copied on every apply. Applying twice must converge to ok.
func TestSourceGlobPruneTemplatedMatchDoesNotFlap(t *testing.T) {
	resource.ResetRepository()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "foo.tmpl"), []byte("value={{.USER}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dst")
	if err := Ensure(dst, WithSourceGlob(filepath.Join(src, "*")), WithPrune); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	written := filepath.Join(dst, "foo")
	if _, err := os.Lstat(written); err != nil {
		t.Fatalf("expected the rendered file at %s: %v", written, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "foo.tmpl")); !os.IsNotExist(err) {
		t.Errorf("the templated source must not land as foo.tmpl, got %v", err)
	}

	resource.ResetRepository()
	resource.ResetReport()
	if err := Ensure(dst, WithSourceGlob(filepath.Join(src, "*")), WithPrune); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	// dst/foo survived the prune: no flapping.
	if _, err := os.Lstat(written); err != nil {
		t.Fatalf("rendered file was pruned: %v", err)
	}
	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	// The count header legitimately contains the word "changed"; the
	// convergence assertion is that no per-note changed line appears.
	summary := buf.String()
	if !strings.Contains(summary, "summary: 2 ok, 0 changed") {
		t.Errorf("second run must be converged, summary:\n%s", summary)
	}
	for _, line := range strings.Split(summary, "\n") {
		if strings.HasPrefix(line, "  changed ") {
			t.Errorf("second run must have no changed notes, summary:\n%s", summary)
		}
	}
}

// TestSourceCopyParamOverrideStableAcrossBlobRoots pins the plan-path
// template param plumbing (task 622): copySourceFile must render tree .tmpl
// files with {{.Param}} = WithSourceBase + "/" + the entry's path relative
// to the synced root — the recipe's declared identity — and never the
// ephemeral blob root the tree is restored from. Two applies with DIFFERENT
// blob roots (as two plan runs produce) must converge instead of flapping
// the rendered checksum every run.
func TestSourceCopyParamOverrideStableAcrossBlobRoots(t *testing.T) {
	resource.ResetRepository()
	base := t.TempDir()
	tmplContent := []byte("param is {{.Param}}\n")

	// Two blob roots, as two plan runs with different plan dirs would
	// extract the same source tree into.
	blob1 := filepath.Join(base, "plan-one", "blobs", "tree")
	blob2 := filepath.Join(base, "plan-two", "blobs", "tree")
	for _, blob := range []string{blob1, blob2} {
		if err := os.MkdirAll(blob, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(blob, "app.conf.tmpl"), tmplContent, 0o640); err != nil {
			t.Fatal(err)
		}
	}

	dst := filepath.Join(base, "dst")
	if err := Ensure(dst, WithSource(blob1), WithSourceBase("assets/testfiles"), WithFileMode(0o640)); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	out := filepath.Join(dst, "app.conf")
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "param is assets/testfiles/app.conf.tmpl\n"; string(got) != want {
		t.Fatalf("rendered content = %q, want %q", got, want)
	}

	// Second apply from the OTHER blob root: the rendered content must be
	// identical (stable Param), so the file is converged, not rewritten.
	resource.ResetReport()
	if err := Ensure(dst, WithSource(blob2), WithSourceBase("assets/testfiles"), WithFileMode(0o640)); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if resource.AnyChanged("File[" + out + "]") {
		t.Fatalf("%s was rewritten by the second apply (blob-path Param flap)", out)
	}
	got2, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != string(got) {
		t.Fatalf("content changed across blob roots: %q vs %q", got2, got)
	}
}

// TestSourceCopyParamWithoutSourceBaseKeepsBlobPath pins the back-compat
// behavior: without WithSourceBase (direct resource use, or plans recorded
// before schema v6) a tree .tmpl file keeps deriving {{.Param}} from its
// mechanical source path.
func TestSourceCopyParamWithoutSourceBaseKeepsBlobPath(t *testing.T) {
	resource.ResetRepository()
	base := t.TempDir()
	blob := filepath.Join(base, "blobs", "tree")
	if err := os.MkdirAll(blob, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blob, "app.conf.tmpl"), []byte("param is {{.Param}}\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(base, "dst")
	if err := Ensure(dst, WithSource(blob), WithFileMode(0o640)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "app.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "param is " + filepath.Join(blob, "app.conf.tmpl") + "\n"; string(got) != want {
		t.Fatalf("rendered content = %q, want %q", got, want)
	}
}
