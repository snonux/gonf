package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCreatesFileWithContentAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")

	if err := Write(path, []byte("atomic content"), 0o640); err != nil {
		t.Fatalf("Write: %v", err)
	}
	requireFile(t, path, "atomic content", 0o640)
	requireNoTempFiles(t, dir)
}

func TestWriteOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Write(path, []byte("new content"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	requireFile(t, path, "new content", 0o600)
	requireNoTempFiles(t, dir)
}

// TestWriteReplacesSymlinkNotItsTarget pins that the rename swaps the
// directory entry: a symlink at path becomes a regular file and the file it
// pointed to is untouched. Callers that mean to edit the link's target
// resolve it first.
func TestWriteReplacesSymlinkNotItsTarget(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link")
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}

	if err := Write(path, []byte("managed"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("%s is %v, want a regular file", path, info.Mode().Type())
	}
	requireFile(t, victim, "victim", 0o644)
}

// TestWriteCleansUpWhenRenameFails pins the error-path guarantee: if the
// final rename cannot succeed (here because a directory occupies the target
// path), the temporary file must be removed.
func TestWriteCleansUpWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}

	err := Write(target, []byte("content"), 0o640)
	if err == nil {
		t.Fatal("expected an error when the target path is a directory")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("error should mention the target path: %v", err)
	}
	requireNoTempFiles(t, dir)
}

// TestWriteFailureLeavesOriginalUntouched pins that a write that cannot
// even create its temporary file (a read-only directory) fails, naming the
// target, and leaves the original content and mode in place.
func TestWriteFailureLeavesOriginalUntouched(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.conf")
	if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := Write(path, []byte("replacement"), 0o644)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Write into a read-only directory err = %v, want an error naming %s", err, path)
	}
	requireFile(t, path, "original", 0o640)
	requireNoTempFiles(t, dir)
}

// TestWriteWithOwnerKeepsOwnOwnership pins that WithOwner naming the
// caller's own ids succeeds without privilege (the chown is skipped when it
// changes nothing) and the result carries that ownership.
func TestWriteWithOwnerKeepsOwnOwnership(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owned")
	own := Owner{UID: os.Getuid(), GID: os.Getgid()}

	if err := Write(path, []byte("x"), 0o644, WithOwner(own)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	requireOwner(t, path, own)
}

// TestWriteWithOwnerAppliesSupplementaryGroup pins that WithOwner really
// chowns the temporary file: a group the caller belongs to but that is not
// its primary one ends up on the target.
func TestWriteWithOwnerAppliesSupplementaryGroup(t *testing.T) {
	gid, ok := otherGroup()
	if !ok {
		t.Skip("the caller has no supplementary group")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "grouped")
	want := Owner{UID: os.Getuid(), GID: gid}

	if err := Write(path, []byte("x"), 0o640, WithOwner(want)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	requireOwner(t, path, want)
	requireFile(t, path, "x", 0o640)
}

// TestWriteWithOwnerFailureLeavesOriginalUntouched pins the refusal of a
// chown the caller may not make: the write fails before the rename, the
// original keeps its content, mode and owner, and no temporary file stays.
func TestWriteWithOwnerFailureLeavesOriginalUntouched(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may chown to any owner")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.conf")
	if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}

	err := Write(path, []byte("replacement"), 0o644, WithOwner(Owner{UID: 0, GID: 0}))
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Write with a root owner err = %v, want an error naming %s", err, path)
	}
	requireFile(t, path, "original", 0o640)
	requireOwner(t, path, Owner{UID: os.Getuid(), GID: os.Getgid()})
	requireNoTempFiles(t, dir)
}

// TestWriteLongBaseName proves temp names stay within NAME_MAX for base
// names that are themselves legal but leave little room for a suffix.
func TestWriteLongBaseName(t *testing.T) {
	dir := t.TempDir()
	// 250 characters: legal as a plain file, but 250+8+10 random characters
	// would exceed NAME_MAX (255) without base truncation.
	target := filepath.Join(dir, strings.Repeat("l", 250))
	probe, err := os.Create(target)
	if err != nil {
		if strings.Contains(err.Error(), "file name too long") {
			t.Skip("filesystem NAME_MAX too small for this test")
		}
		t.Fatalf("probing NAME_MAX: %v", err)
	}
	_ = probe.Close()
	_ = os.Remove(target)

	if err := Write(target, []byte("long name content"), 0o640); err != nil {
		t.Fatalf("Write with a 250-char base name: %v", err)
	}
	requireFile(t, target, "long name content", 0o640)
	requireNoTempFiles(t, dir)
}

// otherGroup returns a group of the caller other than its primary one.
func otherGroup() (int, bool) {
	groups, err := os.Getgroups()
	if err != nil {
		return 0, false
	}
	for _, g := range groups {
		if g != os.Getgid() {
			return g, true
		}
	}
	return 0, false
}

func requireFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("%s = %q, want %q", path, got, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), mode)
	}
}

func requireOwner(t *testing.T, path string, want Owner) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := OwnerOf(info)
	if !ok {
		t.Fatalf("no ownership in the stat of %s", path)
	}
	if got != want {
		t.Errorf("%s owner = %+v, want %+v", path, got, want)
	}
}

func requireNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".gonftmp") {
			t.Errorf("leftover temporary file %s", e.Name())
		}
	}
}
