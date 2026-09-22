package cron

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMain keeps every real (flock) lock taken by this package's tests away
// from root's system parent (/var/run, /var/db) and the real home directory.
//
// In the helper process of TestCrontabLockSerializesSeparateProcesses the
// parent test hands over its lock directory and host name (see
// configureLockHelperProcess). Otherwise the default lock directory is a
// package-wide throwaway root: a safety net only, because a lock leaked
// there by one failing test would block every later test and -count
// iteration. Tests that take the real lock through crontabLockLocation call
// useTestLockDir for a directory of their own (the opt-in live tests,
// GONF_RUN_CRON_TESTS=1, still use this shared root). (Ensure under a
// testseam.FakeCrontab fake uses the in-process lock instead, unless
// testseam.FakeCrontabLock(t, false) keeps the real one.) The lock directory itself does not exist yet: the lock code
// creates it, exercising the production path.
func TestMain(m *testing.M) {
	if os.Getenv(lockHelperEnv) == "1" {
		configureLockHelperProcess()
		os.Exit(m.Run())
	}
	parent, err := os.MkdirTemp("", "gonf-cron-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	crontabLockDirOverride = filepath.Join(parent, "locks")
	code := m.Run()
	_ = os.RemoveAll(parent)
	os.Exit(code)
}

// newLockParent returns a fresh parent with an umask-independent 0700 mode
// and the (not yet existing) lock directory below it.
func newLockParent(t *testing.T) (parent, dir string) {
	t.Helper()
	parent = t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	return parent, filepath.Join(parent, "locks")
}

// useTestLockDir points crontabLockLocation (and so lockCrontab and the
// production acquireCrontabLock) at a lock directory private to this test,
// restored afterwards. A lock another test leaked, or one held by a helper
// process that outlived its test, can then never block this test. It
// returns the (not yet existing) lock directory.
func useTestLockDir(t *testing.T) string {
	t.Helper()
	_, dir := newLockParent(t)
	saved := crontabLockDirOverride
	crontabLockDirOverride = dir
	t.Cleanup(func() { crontabLockDirOverride = saved })
	return dir
}

func euid() uint32 { return uint32(unix.Geteuid()) }

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s must not exist after a failed acquisition (lstat err=%v)", path, err)
	}
}

func assertLockObject(t *testing.T, path string, fileType, perm uint32) {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		t.Fatal(err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != fileType || uint32(stat.Mode)&0o7777 != perm || stat.Uid != euid() {
		t.Fatalf("%s: uid=%d mode=%#o, want uid=%d type=%#o perm=%#o", path, stat.Uid, stat.Mode, euid(), fileType, perm)
	}
}

// at is the lock location used by most tests: dir below a test parent, with
// the same one-level parent creation that ~/.cache gets in production.
func at(dir string) lockLocation { return lockLocation{dir: dir, createParent: true, hostScoped: true} }

// lockFileName is crontabLockFileName for tests that expect no error.
func lockFileName(t *testing.T, userName string) string {
	t.Helper()
	name, err := crontabLockFileName(userName, true)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// withUmask runs fn under mask; the umask is process-wide, which is fine
// because this package's tests do not run in parallel.
func withUmask(mask int, fn func()) {
	old := unix.Umask(mask)
	defer unix.Umask(old)
	fn()
}

// TestCrontabLockSerializesSameTarget keeps the core guarantee: two
// acquisitions for one crontab exclude each other until the first releases,
// while a different crontab name is independent.
func TestCrontabLockSerializesSameTarget(t *testing.T) {
	_, dir := newLockParent(t)
	unlock, err := lockCrontabIn(at(dir), euid(), "target", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockCrontabIn(at(dir), euid(), "target", 30*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("second acquisition of a held lock: err=%v, want timeout", err)
	}
	other, err := lockCrontabIn(at(dir), euid(), "other", 30*time.Millisecond)
	if err != nil {
		t.Fatalf("a different crontab must not contend: %v", err)
	}
	if err := other(); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := lockCrontabIn(at(dir), euid(), "target", time.Second)
	if err != nil {
		t.Fatalf("acquisition after release: %v", err)
	}
	if err := again(); err != nil {
		t.Fatal(err)
	}
}

// TestCrontabLockSeparatesHostsSharingAHome: the host is part of the lock
// file name, so two hosts using one NFS home do not contend, while the same
// host still does.
func TestCrontabLockSeparatesHostsSharingAHome(t *testing.T) {
	saved := lockHostName
	t.Cleanup(func() { lockHostName = saved })
	_, dir := newLockParent(t)
	lockHostName = func() (string, error) { return "hosta", nil }
	unlockA, err := lockCrontabIn(at(dir), euid(), "target", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlockA() }()
	if _, err := lockCrontabIn(at(dir), euid(), "target", 20*time.Millisecond); err == nil {
		t.Fatal("same host did not contend")
	}
	lockHostName = func() (string, error) { return "hostb", nil }
	unlockB, err := lockCrontabIn(at(dir), euid(), "target", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("another host sharing the home contended: %v", err)
	}
	if err := unlockB(); err != nil {
		t.Fatal(err)
	}
}

func TestShortHostNameIsAFileNameComponent(t *testing.T) {
	host, err := shortHostName()
	if err != nil {
		t.Fatal(err)
	}
	if host == "" || strings.ContainsAny(host, "./\x00") {
		t.Fatalf("short host name %q is not a safe file name component", host)
	}
}

// TestCrontabLockReportsUnsupportedLocking: a filesystem that implements no
// flock (EOPNOTSUPP/ENOTSUP) must fail with an explanation, never fall back
// to running unserialised, and leave nothing behind (nobody can hold a lock
// there, so the rollback is safe).
func TestCrontabLockReportsUnsupportedLocking(t *testing.T) {
	for _, errno := range []unix.Errno{unix.EOPNOTSUPP, unix.ENOTSUP} {
		t.Run(errno.Error(), func(t *testing.T) {
			saved := flock
			flock = func(int, int) error { return errno }
			t.Cleanup(func() { flock = saved })
			parent, dir := newLockParent(t)
			cache := filepath.Join(parent, ".cache")
			_, err := lockCrontabIn(at(filepath.Join(cache, "gonf-crontab")), euid(), "target", time.Second)
			if !errors.Is(err, errLockUnsupported) || !errors.Is(err, errno) || !strings.Contains(err.Error(), "does not support file locking") {
				t.Fatalf("err=%v, want the unsupported-locking error wrapping %v", err, errno)
			}
			assertAbsent(t, cache)
			assertAbsent(t, dir)
		})
	}
}

// TestCrontabLockIsUmaskIndependent creates the lock objects under umasks
// that would otherwise leave them too open or read-only; fchmod through the
// descriptor must always produce exactly 0700/0600. A umask that strips the
// owner's own read bit (0777) makes a new directory unopenable for non-root;
// since nothing is ever chmodded by name, that attempt must fail cleanly.
func TestCrontabLockIsUmaskIndependent(t *testing.T) {
	for _, mask := range []int{0, 0o022, 0o077, 0o277, 0o777} {
		t.Run(fmt.Sprintf("umask %#o", mask), func(t *testing.T) {
			_, dir := newLockParent(t)
			var unlock func() error
			var err error
			withUmask(mask, func() { unlock, err = lockCrontabIn(at(dir), euid(), "target", time.Second) })
			if mask&0o400 != 0 && euid() != 0 {
				if err == nil || !strings.Contains(err.Error(), "umask") {
					t.Fatalf("unopenable new directory: err=%v", err)
				}
				assertAbsent(t, dir)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := unlock(); err != nil {
				t.Fatal(err)
			}
			assertLockObject(t, dir, unix.S_IFDIR, 0o700)
			assertLockObject(t, filepath.Join(dir, lockFileName(t, "target")), unix.S_IFREG, 0o600)
		})
	}
}

// TestCrontabLockCreatesMissingCacheDirUmaskProof: a missing ~/.cache is
// created 0700 whatever the umask (it used to end up dr-x------ under 0277)
// and kept; when it cannot be made usable (0777 as non-root) it is removed
// again instead of being left behind as d--------- for other programs.
func TestCrontabLockCreatesMissingCacheDirUmaskProof(t *testing.T) {
	for _, mask := range []int{0o277, 0o777} {
		t.Run(fmt.Sprintf("umask %#o", mask), func(t *testing.T) {
			parent, _ := newLockParent(t)
			cache := filepath.Join(parent, ".cache")
			dir := filepath.Join(cache, "gonf-crontab")
			var unlock func() error
			var err error
			withUmask(mask, func() { unlock, err = lockCrontabIn(at(dir), euid(), "target", time.Second) })
			if mask&0o400 != 0 && euid() != 0 {
				if err == nil {
					t.Fatal("unusable new ~/.cache was accepted")
				}
				assertAbsent(t, cache)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := unlock(); err != nil {
				t.Fatal(err)
			}
			assertLockObject(t, cache, unix.S_IFDIR, 0o700)
			assertLockObject(t, dir, unix.S_IFDIR, 0o700)
		})
	}
}

// TestCrontabLockNeverCreatesSystemParent: root's system parent must
// exist; a location without createParent reports a missing parent instead
// of creating it.
func TestCrontabLockNeverCreatesSystemParent(t *testing.T) {
	parent, _ := newLockParent(t)
	missing := filepath.Join(parent, "run")
	_, err := lockCrontabIn(lockLocation{dir: filepath.Join(missing, "gonf-crontab")}, euid(), "target", time.Second)
	if err == nil || !errors.Is(err, unix.ENOENT) {
		t.Fatalf("missing system parent: err=%v, want ENOENT", err)
	}
	assertAbsent(t, missing)
}

// unsafeLockSeed plants a damaged or hostile object at the lock directory
// dir or lock file path and returns the path the error must name.
type unsafeLockSeed struct {
	name string
	seed func(t *testing.T, dir, path string) (culprit string)
}

func unsafeLockSeeds() []unsafeLockSeed {
	return []unsafeLockSeed{
		{"world-readable directory", func(t *testing.T, dir, _ string) string {
			mkdirMode(t, dir, 0o755)
			return dir
		}},
		{"symlinked directory", func(t *testing.T, dir, _ string) string {
			if err := os.Symlink(t.TempDir(), dir); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		{"directory replaced by a file", func(t *testing.T, dir, _ string) string {
			writeFileMode(t, dir, 0o700)
			return dir
		}},
		{"world-writable lock file", func(t *testing.T, dir, path string) string {
			mkdirMode(t, dir, 0o700)
			writeFileMode(t, path, 0o666)
			return path
		}},
		{"symlinked lock file", func(t *testing.T, dir, path string) string {
			mkdirMode(t, dir, 0o700)
			target := filepath.Join(t.TempDir(), "victim")
			writeFileMode(t, target, 0o600)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	}
}

// TestCrontabLockRejectsUnsafeObjects: a damaged or planted object inside
// the namespace fails closed with an actionable message naming the path, and
// is left untouched (it was not created by this call).
func TestCrontabLockRejectsUnsafeObjects(t *testing.T) {
	for _, testCase := range unsafeLockSeeds() {
		t.Run(testCase.name, func(t *testing.T) {
			_, dir := newLockParent(t)
			path := filepath.Join(dir, lockFileName(t, "target"))
			culprit := testCase.seed(t, dir, path)
			_, err := lockCrontabIn(at(dir), euid(), "target", 10*time.Millisecond)
			if err == nil {
				t.Fatal("unsafe lock object was accepted")
			}
			if want := "remove " + culprit; !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q lacks remediation %q", err, want)
			}
			if _, statErr := os.Lstat(culprit); statErr != nil {
				t.Fatalf("pre-existing %s was removed: %v", culprit, statErr)
			}
		})
	}
}

// TestCrontabLockRejectsForeignOwner models the old preseed attack at the
// verification layer: an existing directory that belongs to another account
// is refused. (In production the parent is private, so such a directory can
// no longer be planted by another account; see the next test.)
func TestCrontabLockRejectsForeignOwner(t *testing.T) {
	_, dir := newLockParent(t)
	mkdirMode(t, dir, 0o700)
	foreign := euid() + 1
	_, err := lockCrontabIn(at(dir), foreign, "target", 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("owned by uid %d, want uid %d", euid(), foreign)) {
		t.Fatalf("foreign-owned lock directory: err=%v", err)
	}
}

// TestCrontabLockReportsNoLocksWithoutRollback: ENOLCK may be transient
// (lock table exhaustion, a lost NFSv4 lock) while another process holds the
// lock, so it fails with an explanation but must NOT unlink the lock file or
// its directory, which would break that holder's exclusion.
func TestCrontabLockReportsNoLocksWithoutRollback(t *testing.T) {
	saved := flock
	flock = func(int, int) error { return unix.ENOLCK }
	t.Cleanup(func() { flock = saved })
	_, dir := newLockParent(t)
	_, err := lockCrontabIn(at(dir), euid(), "target", time.Second)
	if !errors.Is(err, unix.ENOLCK) || errors.Is(err, errLockUnsupported) || !strings.Contains(err.Error(), "no locks available") {
		t.Fatalf("err=%v, want the ENOLCK explanation without errLockUnsupported", err)
	}
	flock = saved
	assertLockObject(t, dir, unix.S_IFDIR, 0o700)
	assertLockObject(t, filepath.Join(dir, lockFileName(t, "target")), unix.S_IFREG, 0o600)
}

// TestCrontabLockRootLockIsHostIndependent: root's system lock never
// includes the host name (a run may rename the host), so the host lookup is
// not even consulted; ~/.cache locks are host-scoped.
func TestCrontabLockRootLockIsHostIndependent(t *testing.T) {
	saved := lockHostName
	lockHostName = func() (string, error) { return "", errors.New("host lookup must not be used") }
	t.Cleanup(func() { lockHostName = saved })
	name, err := crontabLockFileName("root", false)
	if err != nil || strings.Count(name, ".") != 1 {
		t.Fatalf("root lock file name = %q, %v; want <digest>.lock", name, err)
	}
	if _, err := crontabLockFileName("root", true); err == nil {
		t.Fatal("host-scoped name did not consult the host lookup")
	}
}

// TestCrontabLockHostNameIsCachedPerProcess: a rename during the run (the
// lookup starts returning another name) must not move the lock file.
func TestCrontabLockHostNameIsCachedPerProcess(t *testing.T) {
	calls := 0
	lookup := cacheHostName(func() (string, error) {
		calls++
		return fmt.Sprintf("host%d", calls), nil
	})
	first, _ := lookup()
	second, _ := lookup()
	if first != "host1" || second != first || calls != 1 {
		t.Fatalf("lookups = %q, %q after %d calls; want one cached lookup", first, second, calls)
	}
}

// TestCrontabLockRealHostLookupIsCached goes through the production lookup
// composition (newLockHostName, not a test double): even when the underlying
// host name changes between calls, as when a run renames the host, the
// host-scoped lock file name must stay the same within the process.
//
// It installs a FRESH production lookup rather than using the process-wide
// lockHostName: filling that cache with the fake "first" host used to leak
// into every later test (shuffle order), so this process locked
// first.<hash>.lock while the helper process of
// TestCrontabLockSerializesSeparateProcesses locked <realhost>.<hash>.lock.
func TestCrontabLockRealHostLookupIsCached(t *testing.T) {
	savedHostname, savedLookup := osHostname, lockHostName
	t.Cleanup(func() { osHostname, lockHostName = savedHostname, savedLookup })
	lockHostName = newLockHostName()
	calls := 0
	osHostname = func() (string, error) {
		calls++
		if calls == 1 {
			return "first.example.org", nil
		}
		return "second.example.org", nil
	}
	first := lockFileName(t, "target")
	second := lockFileName(t, "target")
	if first != second || !strings.HasPrefix(first, "first.") || calls != 1 {
		t.Fatalf("lock file names %q then %q after %d host lookups; want one cached lookup of host first", first, second, calls)
	}
}

// TestCheckLockParentRule pins the parent rule: owned by the applying uid,
// never world-writable, group-writable only through the caller's private
// group (gid == egid == euid != 0, as plan.checkDirAttrs), and a root-owned
// ~/.cache is refused for a non-root apply with actionable advice.
func TestCheckLockParentRule(t *testing.T) {
	const dir = unix.S_IFDIR
	user := lockIDs{euid: 1001, egid: 1001}
	testCases := []struct {
		name  string
		attrs lockParentAttrs
		ids   lockIDs
		want  string // "" = accepted
	}{
		{"private 0700", lockParentAttrs{1001, 1001, dir | 0o700}, user, ""},
		{"private 0755", lockParentAttrs{1001, 1001, dir | 0o755}, user, ""},
		{"user-private group 0775", lockParentAttrs{1001, 1001, dir | 0o775}, user, ""},
		{"shared group 0775", lockParentAttrs{1001, 100, dir | 0o775}, user, "not your private group"},
		{"egid differs from euid", lockParentAttrs{1001, 1001, dir | 0o775}, lockIDs{euid: 1001, egid: 1001 + 1}, "not your private group"},
		// gid == egid holds here, so only the egid == euid clause refuses it.
		{"gid is egid but egid is not euid", lockParentAttrs{1001, 1002, dir | 0o775}, lockIDs{euid: 1001, egid: 1002}, "not your private group"},
		{"world-writable", lockParentAttrs{1001, 1001, dir | 0o777}, user, "world-writable"},
		{"root-owned ~/.cache", lockParentAttrs{0, 0, dir | 0o755}, user, "chown it back to uid 1001"},
		{"foreign owner", lockParentAttrs{1002, 1002, dir | 0o700}, user, "owned by uid 1002"},
		{"root system parent 0755", lockParentAttrs{0, 0, dir | 0o755}, lockIDs{}, ""},
		{"root never trusts group 0", lockParentAttrs{0, 0, dir | 0o775}, lockIDs{}, "not your private group"},
		{"not a directory", lockParentAttrs{1001, 1001, unix.S_IFREG | 0o700}, user, "not a directory"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := checkLockParent("/p", testCase.attrs, testCase.ids)
			if (testCase.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), testCase.want)) {
				t.Fatalf("err=%v, want %q", err, testCase.want)
			}
		})
	}
}

// TestCrontabLockAcceptsPrivateGroupParent exercises the rule on disk: a
// 0770 parent in the caller's private group (as Go creates ~/.cache under
// umask 002) is accepted; the same parent in a shared group is refused.
func TestCrontabLockAcceptsPrivateGroupParent(t *testing.T) {
	ids := lockIDs{euid: euid(), egid: uint32(unix.Getegid())}
	if !ids.isPrivateGroup(ids.egid) {
		t.Skip("the process has no user-private group")
	}
	parent, dir := newLockParent(t)
	if err := os.Chmod(parent, 0o770); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockCrontabIn(at(dir), euid(), "target", time.Second)
	if err != nil {
		t.Fatalf("private-group parent refused: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	shared := sharedGroup(t, ids.egid)
	parent, dir = newLockParent(t)
	if err := os.Chown(parent, -1, shared); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := lockCrontabIn(at(dir), euid(), "target", time.Second); err == nil || !strings.Contains(err.Error(), "not your private group") {
		t.Fatalf("shared-group parent: err=%v", err)
	}
	assertAbsent(t, dir)
}

// sharedGroup returns a supplementary group of this process other than its
// private group, or skips the test.
func sharedGroup(t *testing.T, private uint32) int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range groups {
		if uint32(gid) != private {
			return gid
		}
	}
	t.Skip("the process has no supplementary group besides its private group")
	return -1
}

// TestCrontabLockRefusesForeignOwnedParent: a parent owned by someone else
// (here "/", owned by root, for a non-root run) is refused before anything
// is created in it. Without createParent it is verified as a system parent,
// so the refusal carries no chown remedy (the ~/.cache chown advice is
// pinned by TestCheckLockParentRule and lock_parent_test.go).
func TestCrontabLockRefusesForeignOwnedParent(t *testing.T) {
	if euid() == 0 {
		t.Skip("needs a non-root euid")
	}
	dir := "/gonf-crontab-test-must-not-exist"
	_, err := lockCrontabIn(lockLocation{dir: dir}, euid(), "target", time.Second)
	assertSystemParentRefusal(t, err, "/", "owned by uid 0")
	assertAbsent(t, dir)
}

// TestCrontabLockRefusesSharedParent: the lock directory must sit below a
// parent that others cannot write, or another account could preseed it or
// swap it for a symlink. A /tmp-like sticky world-writable parent (the old
// /tmp/gonf-crontab-<uid> layout) is refused before anything is created in
// it; the group-write cases are covered by TestCheckLockParentRule and
// TestCrontabLockAcceptsPrivateGroupParent.
func TestCrontabLockRefusesSharedParent(t *testing.T) {
	for _, mode := range []os.FileMode{0o777 | os.ModeSticky, 0o703} {
		t.Run(mode.String(), func(t *testing.T) {
			parent, dir := newLockParent(t)
			if err := os.Chmod(parent, mode); err != nil {
				t.Fatal(err)
			}
			_, err := lockCrontabIn(at(dir), euid(), "target", 10*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), "is world-writable") {
				t.Fatalf("shared parent: err=%v", err)
			}
			assertAbsent(t, dir)
		})
	}
}

// TestCrontabLockFailedSetupLeavesNothingBehind: when a step after creation
// fails, the objects this attempt created are removed again, so a failed
// attempt can never turn into a permanent "unsafe lock" state (the old
// Fchown leak).
func TestCrontabLockFailedSetupLeavesNothingBehind(t *testing.T) {
	t.Run("directory verification", func(t *testing.T) {
		parent, _ := newLockParent(t)
		cache := filepath.Join(parent, ".cache")
		dir := filepath.Join(cache, "gonf-crontab")
		if _, err := lockCrontabIn(at(dir), euid()+1, "target", 10*time.Millisecond); err == nil {
			t.Fatal("verification of a mis-owned new directory succeeded")
		}
		assertAbsent(t, cache)
		unlock, err := lockCrontabIn(at(dir), euid(), "target", time.Second)
		if err != nil {
			t.Fatalf("a later correct attempt was blocked: %v", err)
		}
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("lock file step", func(t *testing.T) {
		saved := lockHostName
		lockHostName = func() (string, error) { return strings.Repeat("h", 300), nil }
		t.Cleanup(func() { lockHostName = saved })
		parent, _ := newLockParent(t)
		cache := filepath.Join(parent, ".cache")
		if _, err := lockCrontabIn(at(filepath.Join(cache, "gonf-crontab")), euid(), "target", 10*time.Millisecond); !errors.Is(err, unix.ENAMETOOLONG) {
			t.Fatalf("err=%v, want ENAMETOOLONG from the lock file step", err)
		}
		assertAbsent(t, cache)
	})
}

// TestCrontabLockRejectsOtherAccountEarly: a non-root run can never change
// another account's crontab, so it must fail before any lock state exists.
func TestCrontabLockRejectsOtherAccountEarly(t *testing.T) {
	if err := requireCrontabAccess("alice", 1001, 1002); err == nil || !strings.Contains(err.Error(), "only root or that account") {
		t.Fatalf("non-root for another account: err=%v", err)
	}
	for _, allowed := range [][2]uint32{{0, 0}, {1001, 0}, {1001, 1001}} {
		if err := requireCrontabAccess("alice", allowed[0], allowed[1]); err != nil {
			t.Fatalf("target uid %d, euid %d: %v", allowed[0], allowed[1], err)
		}
	}
	if euid() == 0 {
		t.Skip("the end-to-end half needs a non-root euid")
	}
	parent := filepath.Dir(useTestLockDir(t))
	if _, err := lockCrontabWithin("root", 10*time.Millisecond); err == nil {
		t.Fatal("non-root lock for root's crontab succeeded")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected run left state in %s: %v (err=%v)", parent, entries, err)
	}
}

// TestCrontabLockLocationIsPrivateToApplyingAccount pins the namespace
// choice: root uses its system parent (never created; see
// TestPrivilegedCrontabLockDirPerPlatform), other accounts their passwd home
// (~/.cache may be created), never a shared temporary directory.
func TestCrontabLockLocationIsPrivateToApplyingAccount(t *testing.T) {
	saved := crontabLockDirOverride
	crontabLockDirOverride = ""
	t.Cleanup(func() { crontabLockDirOverride = saved })

	rootLoc, err := crontabLockLocation(0)
	if err != nil || rootLoc != (lockLocation{dir: privilegedCrontabLockDir(runtime.GOOS)}) {
		t.Fatalf("root lock location = %+v, %v", rootLoc, err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.ParseUint(current.Uid, 10, 32)
	if err != nil || uid == 0 {
		t.Skip("needs a non-root current account")
	}
	userLoc, err := crontabLockLocation(uint32(uid))
	if err != nil {
		t.Fatal(err)
	}
	if want := (lockLocation{dir: filepath.Join(current.HomeDir, ".cache", "gonf-crontab"), createParent: true, hostScoped: true}); userLoc != want {
		t.Fatalf("user lock location = %+v, want %+v", userLoc, want)
	}
	for _, shared := range []string{"/tmp", os.TempDir()} {
		if strings.HasPrefix(userLoc.dir, shared+"/") {
			t.Fatalf("lock dir below shared %s: %q", shared, userLoc.dir)
		}
	}
}

func TestCrontabUserIDResolvesCurrentAccount(t *testing.T) {
	uid, err := crontabUserID(currentCronUser(t))
	if err != nil {
		t.Fatal(err)
	}
	if uid != euid() {
		t.Fatalf("resolved uid %d, want effective uid %d", uid, euid())
	}
}

func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func writeFileMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
