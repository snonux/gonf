package testutil

import (
	"fmt"
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

// PrivateGroupSkipReason returns "" when the caller is a user-private-group
// user by the standard convention (effective gid == effective uid, and not
// root's 0), the only kind of caller for whom group-writable directories of
// their own group are accepted as plan output directories, and otherwise the
// reason the environment cannot exercise that case. It is for tables that build
// a case per environment-dependent scenario: the case stays in the table and
// its subtest skips with the reason (t.Skip on the subtest's own t, never on the
// table's).
func PrivateGroupSkipReason() string {
	if os.Getegid() != os.Geteuid() || os.Geteuid() == 0 {
		return fmt.Sprintf("egid %d, euid %d: not an unprivileged user-private-group setup", os.Getegid(), os.Geteuid())
	}
	return ""
}

// RequirePrivateGroupUser skips the test (call it on the t of the environment-
// dependent test or subtest only) unless PrivateGroupSkipReason is empty. The
// tests never change the process's gid; they skip instead, and the rule itself
// is covered on every account by the synthetic-identity tests of package plan.
func RequirePrivateGroupUser(t testing.TB) {
	t.Helper()
	if reason := PrivateGroupSkipReason(); reason != "" {
		t.Skip(reason)
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

// TryChgrpForeign puts dir into a foreign group (see FindForeignGroup) and
// returns that gid; dir's mode is left as it is. It never touches a testing.T,
// so table builders can call it at top level: when the environment cannot do it
// (no supplementary group, or a chown the kernel refuses, as for an unmapped gid
// in a user namespace) it returns skip, the reason, and the table keeps the
// case for its subtest to skip with that reason.
func TryChgrpForeign(dir string) (gid int, skip string) {
	gid, ok := FindForeignGroup()
	if !ok {
		return -1, "no supplementary group other than the effective gid to chown to"
	}
	if err := os.Chown(dir, -1, gid); err != nil {
		return -1, fmt.Sprintf("cannot chown %s to group %d: %v", dir, gid, err)
	}
	return gid, ""
}

// ChgrpForeign is TryChgrpForeign for a test or subtest that cannot run without
// a foreign group: it skips t, which must be that test's own t (never a
// table-building parent's), when the group cannot be applied.
func ChgrpForeign(t testing.TB, dir string) int {
	t.Helper()
	gid, skip := TryChgrpForeign(dir)
	if skip != "" {
		t.Skip(skip)
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
