package link

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

func TestPresentSymlinkCreateAndIdempotent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link")

	Present(path, WithSymlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("expected link to point to %s, got %s", target, got)
	}

	// Idempotency
	if err := resource.Apply(); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
}

func TestPresentSymlinkRepoints(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	t1 := filepath.Join(dir, "target1")
	t2 := filepath.Join(dir, "target2")
	if err := os.WriteFile(t1, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(t2, []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithSymlink(t1))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply 1 failed: %v", err)
	}

	// Change target
	resource.ResetRepository()
	Present(path, WithSymlink(t2))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply 2 failed: %v", err)
	}

	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != t2 {
		t.Errorf("expected link to repoint to %s, got %s", t2, got)
	}
}

func TestPresentSymlinkReplacesRealFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	if err := os.WriteFile(path, []byte("real file"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithSymlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("expected link to point to %s, got %s", target, got)
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Errorf("expected no .old residue after successful conversion, got %v", err)
	}
}

func TestPresentHardlinkCreateAndIdempotent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link")

	Present(path, WithHardlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	infoLink, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if infoLink.Mode()&os.ModeSymlink != 0 {
		t.Error("expected hardlink, but got symlink")
	}
	// Verify they share the same inode (via a helper or by checking if we can see it's not a symlink and is a file)
	// Since sameInode is internal to the package, we can just check that we can read it.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Errorf("expected content 'hi', got %q", string(got))
	}

	// Idempotency
	if err := resource.Apply(); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
}

// TestPresentHardlinkSymlinkTargetIdempotent guards against a regression
// where hardlinking to a symlink target was never idempotent: os.Link on
// Linux links the symlink entry itself (it does not follow it), but the
// idempotency check used to compare against os.Stat(target), which follows
// the symlink to the file it points at. That mismatch made every apply
// report StatusChanged, even though nothing needed to change, which in turn
// flapped downstream IfChanged/AnyChanged watchers.
func TestPresentHardlinkSymlinkTargetIdempotent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	sl := filepath.Join(dir, "sl")
	if err := os.Symlink(real, sl); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link")

	Present(path, WithHardlink(sl))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	id := "Hardlink[" + path + "]"
	if !resource.AnyChanged(id) {
		t.Fatalf("expected first apply to report changed for %s", id)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Errorf("expected content 'hi', got %q", string(got))
	}

	// Second apply must be a no-op: the hardlink already shares the real
	// file's inode, so the resource must report "ok", not "changed" again.
	if err := resource.Apply(); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
	if resource.AnyChanged(id) {
		t.Errorf("expected second apply to be idempotent (no change) for %s, but it was reported changed", id)
	}

	// A third apply guards against the original bug's replaceWithLink
	// churn, which would have kept flipping the entry (and its .old
	// backup) on every run.
	if err := resource.Apply(); err != nil {
		t.Fatalf("third Apply failed: %v", err)
	}
	if resource.AnyChanged(id) {
		t.Errorf("expected third apply to be idempotent (no change) for %s, but it was reported changed", id)
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Errorf("expected no .old residue after idempotent applies, got %v", err)
	}
}

func TestPresentHardlinkReplacesRealFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	if err := os.WriteFile(path, []byte("real file"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithHardlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "target content" {
		t.Errorf("expected hardlink content %q, got %q", "target content", string(got))
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Errorf("expected no .old residue after successful conversion, got %v", err)
	}
}

func TestPresentHardlinkMissingTarget(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	target := filepath.Join(dir, "nonexistent")

	Present(path, WithHardlink(target))
	if err := resource.Apply(); err == nil {
		t.Error("expected Apply to fail when hardlink target is missing")
	}
}

func TestPresentAbsentSymlink(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	Present(path, IsAbsent)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected symlink %s to be removed", path)
	}
}

func TestAbsentSymlink(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	Absent(path)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected symlink %s to be removed", path)
	}
}

func TestBuildRequiresKindOrAbsent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")

	// Call Present without specifying symlink, hardlink, or absent
	Present(path)
	if err := resource.Apply(); err == nil {
		t.Error("expected Apply to fail when neither kind nor absent is specified")
	}
}

func TestSymlinkRefusesMissingTarget(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	missing := filepath.Join(dir, "does-not-exist")

	Present(path, WithSymlink(missing))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected Apply to fail for missing symlink target")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("broken symlink should not have been created")
	}
}

func TestSymlinkRefusesExistingDanglingLink(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	missing := filepath.Join(dir, "gone")
	if err := os.Symlink(missing, path); err != nil {
		t.Fatal(err)
	}

	Present(path, WithSymlink(missing))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected Apply to fail for existing dangling symlink")
	}
}

func TestSymlinkAllowsRelativeTarget(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link")

	Present(path, WithSymlink("target"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "target" {
		t.Fatalf("got %q", got)
	}
}

func TestLinkReplacePreservesExistingOldBackup(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("real file"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := path + ".old"
	if err := os.WriteFile(old, []byte("USERS BACKUP"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithSymlink(target))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected Apply to refuse converting a real file while a backup exists")
	} else if !strings.Contains(err.Error(), old) {
		t.Errorf("expected the error to name the conflicting backup %s, got %v", old, err)
	}

	// The user's backup must be untouched.
	got, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "USERS BACKUP" {
		t.Errorf("pre-existing backup was modified: %q", string(got))
	}
	// Nothing was replaced: the original entry is still a real file.
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected the original file to be left in place")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "real file" {
		t.Errorf("original file was modified: %q", string(content))
	}
}

func TestHardlinkReplacePreservesExistingOldBackup(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("real file"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := path + ".old"
	if err := os.WriteFile(old, []byte("USERS BACKUP"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithHardlink(target))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected Apply to refuse converting a real file while a backup exists")
	} else if !strings.Contains(err.Error(), old) {
		t.Errorf("expected the error to name the conflicting backup %s, got %v", old, err)
	}

	got, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "USERS BACKUP" {
		t.Errorf("pre-existing backup was modified: %q", string(got))
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected the original file to be left in place")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "real file" {
		t.Errorf("original file was modified: %q", string(content))
	}
}

func TestPresentHardlinkRollsBackWhenLinkCreationFails(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory cannot be hard-linked, so link creation fails after the
	// original entry has been moved aside.
	target := filepath.Join(dir, "subdir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	Present(path, WithHardlink(target))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected Apply to fail when the hardlink cannot be created")
	}

	// The original file must have been moved back from the aside.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user data" {
		t.Errorf("expected the original file to be restored, got %q", string(got))
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Errorf("expected the aside to be gone after rollback, got %v", err)
	}
}

func TestReplaceWithLinkRollsBackOnCreateFailure(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("boom")
	err := replaceWithLink(path, func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the creation error, got %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user data" {
		t.Errorf("expected the original file to be restored, got %q", string(got))
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Errorf("expected the aside to be renamed back, got %v", err)
	}
}

func TestReplaceWithLinkRemovesAsideOnSuccess(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := replaceWithLink(path, func() error { return nil }); err != nil {
		t.Fatalf("replaceWithLink failed: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("expected the entry at path to be gone (create moved it), got %v", err)
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Errorf("expected the aside to be removed on success, got %v", err)
	}
}

// TestMoveAsideNoReplaceRefusesExisting pins the atomic no-replace property
// of moveAsideNoReplace for non-directory entries: when an entry already
// sits at the aside path, the move must fail without touching either the
// aside or the original entry (no clobbering of a backup planted between
// the caller's pre-check and the move). On darwin the move falls back to
// rename, which does overwrite — the documented residual race there.
func TestMoveAsideNoReplaceRefusesExisting(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin moves asides via rename and would clobber the planted backup")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "conf.old")
	if err := os.WriteFile(old, []byte("PLANTED"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := moveAsideNoReplace(path, old)
	if err == nil {
		t.Fatal("expected the move to refuse an entry at the aside path")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("expected an EEXIST-style error, got %v", err)
	}
	if !strings.Contains(err.Error(), old) {
		t.Errorf("expected the error to name the aside %s, got %v", old, err)
	}

	// The planted backup must be untouched.
	got, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "PLANTED" {
		t.Errorf("the planted backup was clobbered: %q", string(got))
	}
	// The original entry must still be a real file at path.
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		t.Errorf("expected the original file to be untouched, got mode %v", info.Mode())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Errorf("the original file was modified: %q", string(content))
	}
}

func TestMoveAsideNoReplaceFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "conf.old")

	if err := moveAsideNoReplace(path, old); err != nil {
		t.Fatalf("moveAsideNoReplace failed: %v", err)
	}

	// The aside carries the original content (hardlink to the same inode),
	// and the original name is gone.
	got, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("expected the aside to hold the original content, got %q", string(got))
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("expected the original name to be gone, got %v", err)
	}
}

func TestMoveAsideNoReplaceSymlinkEntry(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "conf")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "conf.old")

	if err := moveAsideNoReplace(path, old); err != nil {
		t.Fatalf("moveAsideNoReplace failed: %v", err)
	}

	// The aside must be the symlink ENTRY itself (never its target's data):
	// Lstat sees a symlink and Readlink preserves the raw target.
	info, err := os.Lstat(old)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected the aside to be a symlink, got mode %v", info.Mode())
	}
	got, err := os.Readlink(old)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("expected the aside symlink to point to %s, got %s", target, got)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("expected the original name to be gone, got %v", err)
	}
}

func TestMoveAsideNoReplaceDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "conf.old")

	if err := moveAsideNoReplace(path, old); err != nil {
		t.Fatalf("moveAsideNoReplace failed: %v", err)
	}

	// Directories are moved by rename (link(2) cannot hardlink them).
	info, err := os.Lstat(old)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Errorf("expected the aside to be a directory, got mode %v", info.Mode())
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("expected the original name to be gone, got %v", err)
	}
}

func TestPresentSymlinkReplacesEmptyDir(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithSymlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("expected link to point to %s, got %s", target, got)
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Errorf("expected no .old residue after successful conversion, got %v", err)
	}
}

func TestPresentSymlinkReplacesNonEmptyDirKeepsBackup(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(path, "data")
	if err := os.WriteFile(inside, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, WithSymlink(target))
	// The link is created, but the non-empty directory aside cannot be
	// removed, so the conversion reports an error instead of rm -rf'ing
	// the user's data.
	old := path + ".old"
	applyErr := resource.Apply()
	if applyErr == nil {
		t.Fatal("expected Apply to fail when the aside cannot be removed")
	}
	// The error must name the backup path so the user can resolve it.
	if !strings.Contains(applyErr.Error(), old) {
		t.Errorf("error should name the backup path %s: %v", old, applyErr)
	}
	if _, err := os.Readlink(path); err != nil {
		t.Fatalf("expected the symlink to have been created: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(old, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "precious" {
		t.Errorf("the user's directory data must be preserved under %s, got %q", old, string(got))
	}

	// A retry converges: the idempotency check recognizes the created link
	// before the aside assert runs, so the run succeeds with the residue
	// still present (documented in docs/file-dir-link.md).
	resource.ResetRepository()
	resource.ResetReport()
	Present(path, WithSymlink(target))
	if err := resource.Apply(); err != nil {
		t.Fatalf("retry after the aside-removal failure must converge: %v", err)
	}
	if _, err := os.Lstat(old); err != nil {
		t.Errorf("expected the backup to still be present after the converged retry: %v", err)
	}
}

func TestPresentSymlinkRepointKeepsOldBackup(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	t1 := filepath.Join(dir, "target1")
	t2 := filepath.Join(dir, "target2")
	if err := os.WriteFile(t1, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(t2, []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t1, path); err != nil {
		t.Fatal(err)
	}
	old := path + ".old"
	if err := os.WriteFile(old, []byte("USERS BACKUP"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Repointing an existing symlink never uses the .old aside, so the
	// pre-existing backup is untouched.
	Present(path, WithSymlink(t2))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != t2 {
		t.Errorf("expected link to repoint to %s, got %s", t2, got)
	}
	data, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "USERS BACKUP" {
		t.Errorf("repoint must not touch the backup: %q", string(data))
	}
}

// TestDryRunRefusesConversionWithExistingOldBackup pins that the dry-run
// preview surfaces the same refusal a real apply would: a pre-existing
// path+".old" must fail the dry-run before any change (the assert runs
// before the DryRun branch in both replace flows).
func TestDryRunRefusesConversionWithExistingOldBackup(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := path + ".old"
	if err := os.WriteFile(old, []byte("USERS BACKUP"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}

	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	Present(path, WithSymlink(target))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected the dry-run to refuse the conversion")
	}
	got, err := os.ReadFile(old)
	if err != nil || string(got) != "USERS BACKUP" {
		t.Errorf("the user's backup must be untouched: %v %q", err, got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the dry-run must not convert the file: %v", err)
	}
}
