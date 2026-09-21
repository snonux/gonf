package cron

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Tests for the advice given when an existing lock parent (~/.cache) or
// Gonf's own lock directory cannot be opened. ~/.cache is shared with every
// other application and may need root to repair, so its errors must never
// suggest removing it; Gonf's own lock objects may be removed and recreated.

// newCacheLocation returns a fresh ~/.cache path (not yet existing) inside
// a private temporary home and the lock location below it. Nothing touches
// the real home directory.
func newCacheLocation(t *testing.T) (cache string, loc lockLocation) {
	t.Helper()
	home, _ := newLockParent(t)
	cache = filepath.Join(home, ".cache")
	return cache, at(filepath.Join(cache, "gonf-crontab"))
}

// restoreModeOnCleanup makes path traversable again after the test, so the
// t.TempDir cleanup (registered earlier, so it runs later) can remove it.
func restoreModeOnCleanup(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

// assertParentAdvice checks a parent error: it keeps the open errno, names
// the parent, carries the wanted advice and never suggests removing it.
func assertParentAdvice(t *testing.T, err error, errno unix.Errno, cache, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("unusable lock parent %s was accepted", cache)
	}
	if !errors.Is(err, errno) {
		t.Fatalf("err=%v, want errno %v", err, errno)
	}
	msg := err.Error()
	if !strings.Contains(msg, cache) || !strings.Contains(msg, want) {
		t.Fatalf("error %q lacks %q about %s", msg, want, cache)
	}
	if strings.Contains(msg, "remove") {
		t.Fatalf("error %q advises removing the shared lock parent", msg)
	}
	if _, statErr := os.Lstat(cache); statErr != nil {
		t.Fatalf("pre-existing %s was removed: %v", cache, statErr)
	}
}

// TestCrontabLockParentIsARegularFile: a regular file where ~/.cache should
// be is reported as "not a directory", not with a remove remedy.
func TestCrontabLockParentIsARegularFile(t *testing.T) {
	cache, loc := newCacheLocation(t)
	writeFileMode(t, cache, 0o600)
	_, err := lockCrontabIn(loc, euid(), "target", 10*time.Millisecond)
	assertParentAdvice(t, err, unix.ENOTDIR, cache, "is not a directory")
}

// TestCrontabLockParentDeniesItsOwner: an own ~/.cache with mode 0000 needs
// its permissions restored, not removal. Root bypasses the mode, so skip.
func TestCrontabLockParentDeniesItsOwner(t *testing.T) {
	if euid() == 0 {
		t.Skip("root is not denied by mode 0000")
	}
	cache, loc := newCacheLocation(t)
	mkdirMode(t, cache, 0)
	restoreModeOnCleanup(t, cache)
	_, err := lockCrontabIn(loc, euid(), "target", 10*time.Millisecond)
	assertParentAdvice(t, err, unix.EACCES, cache, "chmod u+rwx it")
}

// fakeParentOwner0700 makes the lock parent inspection report owner uid and
// permission bits 0700 for the rest of the test, keeping the real fstatat
// type bits (the tests use a real directory), so owners a non-root test
// cannot chown to are reachable. Only untyped constants touch stat.Mode,
// which is uint32 on Linux but uint16 on FreeBSD and darwin.
func fakeParentOwner0700(t *testing.T, uid uint32) {
	t.Helper()
	saved := statLockParentEntry
	t.Cleanup(func() { statLockParentEntry = saved })
	statLockParentEntry = func(dirFD int, name string, stat *unix.Stat_t) error {
		if err := saved(dirFD, name, stat); err != nil {
			return err
		}
		stat.Uid = uid
		stat.Mode = stat.Mode&^0o7777 | 0o700
		return nil
	}
}

// TestCrontabLockParentOwnedByRoot: a root-owned 0700 ~/.cache (left behind
// by sudo with HOME kept) gets checkLockParent's "chown it back" advice. A
// non-root test cannot chown to root, so a mode-0000 own directory produces
// the real EACCES and the stat seam reports the root owner and 0700 mode.
func TestCrontabLockParentOwnedByRoot(t *testing.T) {
	if euid() == 0 {
		t.Skip("root is not denied by mode 0000")
	}
	cache, loc := newCacheLocation(t)
	mkdirMode(t, cache, 0)
	restoreModeOnCleanup(t, cache)
	fakeParentOwner0700(t, 0)
	_, err := lockCrontabIn(loc, euid(), "target", 10*time.Millisecond)
	want := fmt.Sprintf("owned by uid 0, not by the applying uid %d, so Gonf cannot keep its lock there; chown it back to uid %d", euid(), euid())
	assertParentAdvice(t, err, unix.EACCES, cache, want)
}

// TestCrontabLockOwnDirKeepsRemoveRemedy: Gonf's own lock directory lives in
// the applying account's namespace and holds nothing else, so an unopenable
// one still gets the remove remedy (the verification failures are covered by
// TestCrontabLockRejectsUnsafeObjects).
func TestCrontabLockOwnDirKeepsRemoveRemedy(t *testing.T) {
	if euid() == 0 {
		t.Skip("root is not denied by mode 0000")
	}
	_, dir := newLockParent(t)
	mkdirMode(t, dir, 0)
	restoreModeOnCleanup(t, dir)
	_, err := lockCrontabIn(at(dir), euid(), "target", 10*time.Millisecond)
	if err == nil || !errors.Is(err, unix.EACCES) || !strings.Contains(err.Error(), "remove "+dir) {
		t.Fatalf("unopenable own lock directory: err=%v, want EACCES with remove %s", err, dir)
	}
}

// TestCrontabLockParentSymlinkToDeniedDir: a relocated ~/.cache (a symlink)
// whose target denies its owner is judged by the target, as openParent
// follows it: the advice is the chmod one, not "is not a directory" for the
// link itself.
func TestCrontabLockParentSymlinkToDeniedDir(t *testing.T) {
	if euid() == 0 {
		t.Skip("root is not denied by mode 0000")
	}
	cache, loc := newCacheLocation(t)
	target := filepath.Join(t.TempDir(), "cache")
	mkdirMode(t, target, 0)
	restoreModeOnCleanup(t, target)
	if err := os.Symlink(target, cache); err != nil {
		t.Fatal(err)
	}
	_, err := lockCrontabIn(loc, euid(), "target", 10*time.Millisecond)
	assertParentAdvice(t, err, unix.EACCES, cache, "chmod u+rwx it")
}

// TestCrontabLockParentDanglingSymlink: a ~/.cache symlink to nothing cannot
// be inspected, so the plain open error is reported, still without advising
// removal, and the link is left in place.
func TestCrontabLockParentDanglingSymlink(t *testing.T) {
	cache, loc := newCacheLocation(t)
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), cache); err != nil {
		t.Fatal(err)
	}
	_, err := lockCrontabIn(loc, euid(), "target", 10*time.Millisecond)
	assertParentAdvice(t, err, unix.ENOENT, cache, "open "+cache)
}

// TestCrontabLockParentDeniedDespiteOwnerBits: an own 0700 ~/.cache that is
// still denied (as by SELinux or AppArmor; simulated with a real mode-0000
// directory and the stat hook) gets no chmod advice, which could not help.
func TestCrontabLockParentDeniedDespiteOwnerBits(t *testing.T) {
	if euid() == 0 {
		t.Skip("root is not denied by mode 0000")
	}
	cache, loc := newCacheLocation(t)
	mkdirMode(t, cache, 0)
	restoreModeOnCleanup(t, cache)
	fakeParentOwner0700(t, euid())
	_, err := lockCrontabIn(loc, euid(), "target", 10*time.Millisecond)
	assertParentAdvice(t, err, unix.EACCES, cache, "open "+cache)
	if strings.Contains(err.Error(), "chmod") {
		t.Fatalf("error %q gives chmod advice for a 0700 owner-accessible parent", err)
	}
}

// TestCrontabLockParentSymlinkIsFollowed: a relocated ~/.cache (a symlink to
// a private directory) is accepted, as openParent documents, and the lock
// directory is created in the target.
func TestCrontabLockParentSymlinkIsFollowed(t *testing.T) {
	cache, loc := newCacheLocation(t)
	target, _ := newLockParent(t)
	if err := os.Symlink(target, cache); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockCrontabIn(loc, euid(), "target", time.Second)
	if err != nil {
		t.Fatalf("symlinked lock parent was refused: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	assertLockObject(t, filepath.Join(target, "gonf-crontab"), unix.S_IFDIR, 0o700)
}
