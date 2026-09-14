package dir

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	. "github.com/snonux/gonf/api/options"
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err == nil {
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
	if err := resource.Apply(); err == nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
		if err := resource.Apply(); err != nil {
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
		if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Now apply with prune
	resource.ResetRepository()
	Present(dst, WithSource(src), WithPrune)
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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

	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	Present(dst, WithSource(src), WithPrune)
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	if err := resource.Apply(); err != nil {
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
	base := make([]Option, 2, 8)
	base[0] = WithMode(0o755)
	base[1] = WithPrune
	before := make([]Option, len(base))
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
	if err := resource.Apply(); err != nil {
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
