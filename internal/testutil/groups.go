package testutil

import (
	"os"
	"syscall"
	"testing"
)

// MkdirMode creates dir with exactly mode and the caller's effective group.
// os.Mkdir is subject to the umask, so it chmods afterwards; the explicit chown
// keeps the group deterministic even when the parent is setgid or a BSD-style
// directory that hands its own group to new entries. Plan output directories
// are judged by owner, mode and group, so tests must control all three.
func MkdirMode(t testing.TB, dir string, mode os.FileMode) string {
	t.Helper()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return dir
}

// RequirePrivateGroupUser skips the test unless the caller is a user-private
// group user by the standard convention (effective gid == effective uid), the
// only kind of caller for whom group-writable directories of their own group
// are accepted as plan output directories. The tests never change the
// process's gid; they skip instead.
func RequirePrivateGroupUser(t testing.TB) {
	t.Helper()
	if os.Getegid() != os.Geteuid() {
		t.Skipf("egid %d != euid %d: not a user-private-group setup", os.Getegid(), os.Geteuid())
	}
}

// FindForeignGroup returns a group id the caller belongs to (a supplementary
// group) that is not its effective gid, so chown-ing a directory to it is
// allowed without privilege yet makes it a group that is not the caller's
// private group. ok is false when the caller has no such group.
func FindForeignGroup() (gid int, ok bool) {
	groups, err := os.Getgroups()
	if err != nil {
		return -1, false
	}
	for _, g := range groups {
		if g != os.Getegid() {
			return g, true
		}
	}
	return -1, false
}

// ForeignGroup is FindForeignGroup for tests that cannot run without such a
// group: it skips the test when there is none.
func ForeignGroup(t testing.TB) int {
	t.Helper()
	gid, ok := FindForeignGroup()
	if !ok {
		t.Skip("no supplementary group other than the effective gid to chown to")
	}
	return gid
}

// ChgrpForeign puts dir into a foreign group (see ForeignGroup) and returns
// that gid; dir's mode is left as it is.
func ChgrpForeign(t testing.TB, dir string) int {
	t.Helper()
	gid := ForeignGroup(t)
	if err := os.Chown(dir, -1, gid); err != nil {
		t.Skipf("cannot chown %s to group %d: %v", dir, gid, err)
	}
	return gid
}

// WithUmask runs fn under umask mask and restores the previous umask. The
// umask is process-wide, so callers must not be parallel tests.
func WithUmask(mask int, fn func()) {
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	fn()
}
