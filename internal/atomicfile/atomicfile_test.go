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

// TestWriteWithOwnerKeepsSetGIDAfterChown pins that WithOwner really
// chowns the temporary file, and chowns it before the chmod: a group the
// caller belongs to but that is not its primary one ends up on the target
// together with a set-gid bit, which a chown after the chmod would clear.
func TestWriteWithOwnerKeepsSetGIDAfterChown(t *testing.T) {
	gid, ok := otherGroup()
	if !ok {
		t.Skip("the caller has no supplementary group")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "grouped")
	want := Owner{UID: os.Getuid(), GID: gid}
	mode := 0o750 | os.ModeSetgid

	if err := Write(path, []byte("x"), mode, WithOwner(want)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	requireOwner(t, path, want)
	requireMode(t, path, mode)
}

// TestWriteAttributesSetBeforeRename pins that the temporary file carries
// its final content, set-id mode and ownership while still unrenamed, so
// the target never shows up with the writer's ownership or a wrong mode.
func TestWriteAttributesSetBeforeRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "setid")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	own := Owner{UID: os.Getuid(), GID: os.Getgid()}
	mode := 0o750 | os.ModeSetuid | os.ModeSetgid
	var observed int
	restore := ObserveBeforeRenameForTest(func(tmp string) {
		observed++
		requireFile(t, tmp, "new", mode.Perm())
		requireMode(t, tmp, mode)
		requireOwner(t, tmp, own)
		requireFile(t, path, "old", 0o600)
	})
	defer restore()

	if err := Write(path, []byte("new"), mode, WithOwner(own)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if observed != 1 {
		t.Fatalf("observer ran %d times, want 1", observed)
	}
	requireMode(t, path, mode)
	requireOwner(t, path, own)
}

// TestWriteWithOwnerUnsetIDs pins os.Chown's -1 convention: an unset id
// stays what the new temporary file got, so WithOwner(-1, -1) needs no
// privilege at all. A new file's group is the directory's on the BSDs and
// darwin (and under a set-gid directory on Linux), the writer's otherwise;
// the directory's own group is the same expectation everywhere, since the
// test created it.
func TestWriteWithOwnerUnsetIDs(t *testing.T) {
	dir := t.TempDir()
	created := newFileOwner(t, dir)
	for name, tt := range map[string]struct{ owner, want Owner }{
		"both unset": {Owner{UID: -1, GID: -1}, created},
		"uid unset":  {Owner{UID: -1, GID: os.Getgid()}, Owner{UID: os.Getuid(), GID: os.Getgid()}},
		"gid unset":  {Owner{UID: os.Getuid(), GID: -1}, created},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := Write(path, []byte("x"), 0o640, WithOwner(tt.owner)); err != nil {
			t.Fatalf("%s: Write: %v", name, err)
		}
		requireOwner(t, path, tt.want)
	}
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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original, ok := OwnerOf(info)
	if !ok {
		t.Fatalf("no ownership in the stat of %s", path)
	}

	err = Write(path, []byte("replacement"), 0o644, WithOwner(Owner{UID: 0, GID: 0}))
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Write with a root owner err = %v, want an error naming %s", err, path)
	}
	requireFile(t, path, "original", 0o640)
	requireOwner(t, path, original)
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

// newFileOwner is the ownership a file the caller creates in dir gets: the
// caller's uid and dir's group, which the caller's own new directory shares
// with the files in it on every supported OS.
func newFileOwner(t *testing.T, dir string) Owner {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := OwnerOf(info)
	if !ok {
		t.Fatalf("no ownership in the stat of %s", dir)
	}
	return Owner{UID: os.Getuid(), GID: owner.GID}
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

func requireMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	const bits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if got := info.Mode() & bits; got != mode {
		t.Errorf("%s mode = %v, want %v", path, got, mode)
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
