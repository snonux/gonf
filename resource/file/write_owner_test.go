package file

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	. "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/atomicfile"
	"github.com/snonux/gonf/resource"
)

// TestEnsureSetIDFileOwnedBeforeRename pins that a changed File's
// temporary file already carries the configured owner, group and set-id
// mode before the rename (task bb review): the managed path never shows a
// set-id file owned by the writer.
func TestEnsureSetIDFileOwnedBeforeRename(t *testing.T) {
	resource.ResetRepository()
	uname, gidStr, _ := currentOwnerForTest(t)
	gid, err := strconv.Atoi(gidStr)
	if err != nil {
		t.Fatal(err)
	}
	requireTempOwnedBeforeRename(t, uname, gid, 0o6750)
}

// TestEnsureSupplementaryGroupSetGIDBeforeRename is the same with a group
// that differs from the writer's, so the temporary file's chown really
// runs (and runs before its chmod, or the set-gid bit would be cleared).
func TestEnsureSupplementaryGroupSetGIDBeforeRename(t *testing.T) {
	resource.ResetRepository()
	uname, _, _ := currentOwnerForTest(t)
	gid, ok := supplementaryGroupForTest()
	if !ok {
		t.Skip("the caller has no supplementary group")
	}
	requireTempOwnedBeforeRename(t, uname, gid, 0o2750)
}

func requireTempOwnedBeforeRename(t *testing.T, uname string, gid int, rawMode os.FileMode) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "setid")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	const modeBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	want := os.FileMode(rawMode.Perm())
	if rawMode&0o4000 != 0 {
		want |= os.ModeSetuid
	}
	if rawMode&0o2000 != 0 {
		want |= os.ModeSetgid
	}
	check := func(p string) {
		t.Helper()
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & modeBits; got != want {
			t.Errorf("%s mode = %v, want %v", p, got, want)
		}
		if uid, g := fileUIDGid(t, p); uid != os.Getuid() || g != gid {
			t.Errorf("%s owner = %d:%d, want %d:%d", p, uid, g, os.Getuid(), gid)
		}
	}
	var observed int
	restore := atomicfile.ObserveBeforeRenameForTest(func(tmp string) {
		observed++
		check(tmp)
	})
	defer restore()

	err := Ensure(path, WithContent("new"), WithOwner(uname), WithGroup(strconv.Itoa(gid)), WithMode(rawMode))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if observed != 1 {
		t.Fatalf("temporary file observed %d times, want 1", observed)
	}
	check(path)
}

// TestEnsureUnresolvableOwnerLeavesFileUntouched pins that the owner is
// resolved before the write: an unknown group fails the apply with the old
// content still in place and no temporary file left, instead of replacing
// the content and failing only on the chown afterwards.
func TestEnsureUnresolvableOwnerLeavesFileUntouched(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path, WithContent("new"), WithGroup("gonf-no-such-group-8f3a")); err == nil {
		t.Fatal("Ensure with an unknown group succeeded")
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Errorf("%s = %q, want the old content", path, got)
	}
	assertNoLeftoverTempFiles(t, dir)
}

// TestTempOwner pins the ownership handed to the temporary file: the
// resolved configured ids, -1 for an unset one, none when both are unset.
func TestTempOwner(t *testing.T) {
	uname, gidStr, _ := currentOwnerForTest(t)
	gid, err := strconv.Atoi(gidStr)
	if err != nil {
		t.Fatal(err)
	}
	f := &File{user: uname, group: gidStr}
	if owner, ok, err := f.tempOwner(); err != nil || !ok || owner != (atomicfile.Owner{UID: os.Getuid(), GID: gid}) {
		t.Errorf("tempOwner() = %+v, %v, %v; want uid %d gid %d", owner, ok, err, os.Getuid(), gid)
	}
	f = &File{group: gidStr}
	if owner, ok, err := f.tempOwner(); err != nil || !ok || owner != (atomicfile.Owner{UID: -1, GID: gid}) {
		t.Errorf("group-only tempOwner() = %+v, %v, %v; want uid -1 gid %d", owner, ok, err, gid)
	}
	if _, ok, err := (&File{}).tempOwner(); err != nil || ok {
		t.Errorf("unset tempOwner() ok = %v, err = %v; want no owner", ok, err)
	}
}

// supplementaryGroupForTest returns a group of the caller other than its
// primary one.
func supplementaryGroupForTest() (int, bool) {
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
