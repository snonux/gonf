package cron

// Descriptor-based creation and verification of the crontab lock objects
// (lock.go explains where they live and why).
//
// Rules, each against a specific attack or failure:
//   - Nothing is ever chmodded or chowned BY NAME. A new object is created
//     (mkdirat, or openat O_CREAT|O_EXCL), opened with O_NOFOLLOW, confirmed
//     by fstat to be the type and owner this process just created, and only
//     then fchmodded through its descriptor. This fixes the mode regardless
//     of the umask without ever following a swapped-in symlink.
//   - A umask that strips the owner's own read bit (e.g. 0777) makes a new
//     directory unopenable for a non-root owner; since chmod by name is
//     ruled out, that attempt fails with an explanation and is rolled back.
//   - Existing objects are verified by fstat (type, owner, exact mode) and
//     never repaired or removed: they may be another process's live lock.
//   - An attempt that fails removes exactly the objects IT created, newest
//     first, so a failure never leaves state that blocks later applies.
//     After a lock file is verified, other processes may start using it, so
//     from then on only EOPNOTSUPP/ENOTSUP (the filesystem implements no
//     locking, so nobody can hold the lock) still rolls back. Contention and
//     ENOLCK (possibly transient while another process holds the lock) keep
//     everything in place.
//     Unlinking a lock file that someone may hold would let a newcomer lock
//     a fresh file while the holder keeps the orphan, breaking exclusion.

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/snonux/gonf/internal/dirperm"
)

// statLockParentEntry inspects (following symlinks) the lock parent entry
// name inside dirFD after it could not be opened, to word advice specific
// to why. A variable so tests can report an owner (root) that a non-root
// test cannot really chown to.
var statLockParentEntry = func(dirFD int, name string, stat *unix.Stat_t) error {
	return unix.Fstatat(dirFD, name, stat, 0)
}

// lockSetup tracks one acquisition attempt: the directory descriptors to
// close afterwards and the objects it created, for rollback.
type lockSetup struct {
	ownerUID uint32
	fds      []int
	created  []createdLockObject
}

// createdLockObject is an entry this attempt created, named relative to
// the (still open) descriptor of its parent directory.
type createdLockObject struct {
	parentFD int
	name     string
	flags    int // 0 for a file, unix.AT_REMOVEDIR for a directory
}

func (s *lockSetup) track(fd int) { s.fds = append(s.fds, fd) }

func (s *lockSetup) remember(parentFD int, name string, flags int) {
	s.created = append(s.created, createdLockObject{parentFD: parentFD, name: name, flags: flags})
}

// keepCreated commits the created objects: they are valid and may already
// be in use by other processes, so rollback must no longer remove them.
func (s *lockSetup) keepCreated() { s.created = nil }

// rollback removes this attempt's objects, newest first. A directory is
// only removed while empty (unlinkat AT_REMOVEDIR), so content another
// process put there in the meantime is never lost.
func (s *lockSetup) rollback() {
	for i := len(s.created) - 1; i >= 0; i-- {
		object := s.created[i]
		_ = unix.Unlinkat(object.parentFD, object.name, object.flags)
	}
	s.created = nil
}

func (s *lockSetup) closeAll() {
	for _, fd := range s.fds {
		_ = unix.Close(fd)
	}
	s.fds = nil
}

// acquire opens (creating as needed) the parent, lock directory and lock
// file of loc and flocks the file. The returned descriptor is the caller's.
func (s *lockSetup) acquire(loc lockLocation, name string, timeout time.Duration) (int, error) {
	parentFD, err := s.openParent(filepath.Dir(loc.dir), loc.createParent)
	if err != nil {
		return -1, err
	}
	dirFD, err := s.openLockDir(parentFD, loc.dir)
	if err != nil {
		return -1, err
	}
	path := filepath.Join(loc.dir, name)
	fd, err := s.openLockFile(dirFD, path)
	if err != nil {
		return -1, err
	}
	if err := flockWithin(fd, path, timeout); err != nil {
		_ = unix.Close(fd)
		if !errors.Is(err, errLockUnsupported) {
			s.keepCreated()
		}
		return -1, err
	}
	s.keepCreated()
	return fd, nil
}

// openParent opens and verifies the directory holding the lock directory.
// With create it may create that one level (~/.cache) inside its existing
// parent (the home directory); without, a missing parent (/var/run) is an
// error. An existing parent may be reached through a symlink (/var/run ->
// /run, a relocated ~/.cache): what matters is the directory it resolves to,
// which verifyLockParent checks through the descriptor.
func (s *lockSetup) openParent(parent string, create bool) (int, error) {
	if !create {
		fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return -1, fmt.Errorf("open crontab lock parent %s: %w", parent, err)
		}
		s.track(fd)
		return fd, verifyLockParent(fd, parent)
	}
	grandparent := filepath.Dir(parent)
	gpFD, err := unix.Open(grandparent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", grandparent, err)
	}
	s.track(gpFD)
	fd, err := s.openOrCreateDir(gpFD, parent, true)
	if err != nil {
		return -1, err
	}
	return fd, verifyLockParent(fd, parent)
}

// verifyLockParent checks, through its descriptor, the directory holding
// the lock directory; see checkLockParent for the rule.
func verifyLockParent(fd int, path string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect crontab lock parent %s: %w", path, err)
	}
	ids := lockIDs{euid: uint32(unix.Geteuid()), egid: uint32(unix.Getegid())}
	return checkLockParent(path, lockParentAttrs{uid: stat.Uid, gid: stat.Gid, mode: uint32(stat.Mode)}, ids)
}

// lockParentAttrs are the fstat fields checkLockParent looks at.
type lockParentAttrs struct {
	uid, gid, mode uint32
}

// lockIDs are the process's effective ids.
type lockIDs struct {
	euid, egid uint32
}

// isPrivateGroup applies gonf's shared user-private-group convention
// (dirperm.IDs.IsPrivateGroup): a group is the caller's private group only
// when gid == egid == euid and it is not gid 0 ("wheel" on the BSDs), so
// root never accepts a group-writable parent.
func (ids lockIDs) isPrivateGroup(gid uint32) bool {
	return dirperm.IDs{EUID: ids.euid, EGID: ids.egid}.IsPrivateGroup(gid)
}

// checkLockParent requires a directory owned by the applying account (root
// for /var/run; a ~/.cache owned by root or anyone else is refused, as a
// non-root apply could not create its lock there anyway). It must not be
// world-writable, and group write is accepted only for the account's private
// group: Go and umask-002 systems create ~/.cache as 0775 that way. Only then
// can no other account create, rename or replace the lock directory inside
// it, which rules out preseeding it or swapping it for a symlink. Shared
// directories such as /tmp are therefore refused outright.
func checkLockParent(path string, a lockParentAttrs, ids lockIDs) error {
	switch {
	case a.mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("crontab lock parent %s is not a directory", path)
	case a.uid != ids.euid:
		return fmt.Errorf("crontab lock parent %s is owned by uid %d, not by the applying uid %d, so Gonf cannot keep its lock there; chown it back to uid %d", path, a.uid, ids.euid, ids.euid)
	case a.mode&0o002 != 0:
		return fmt.Errorf("crontab lock parent %s is world-writable (mode %#o), so other accounts could preseed the lock; chmod o-w it", path, a.mode&0o7777)
	case a.mode&0o020 != 0 && !ids.isPrivateGroup(a.gid):
		return fmt.Errorf("crontab lock parent %s is writable by group %d, which is not your private group (mode %#o); chmod g-w it", path, a.gid, a.mode&0o7777)
	}
	return nil
}

// openLockDir opens (creating if needed) the lock directory below the
// verified parentFD and verifies it as the owner's private 0700 directory.
// An existing symlink is refused (O_NOFOLLOW), never followed.
func (s *lockSetup) openLockDir(parentFD int, dir string) (int, error) {
	fd, err := s.openOrCreateDir(parentFD, dir, false)
	if err != nil {
		return -1, err
	}
	if err := verifyLockObject(fd, unix.S_IFDIR, 0o700, s.ownerUID); err != nil {
		return -1, fmt.Errorf("unsafe crontab lock directory %s: %w%s", dir, err, lockRemedy(dir))
	}
	return fd, nil
}

// openOrCreateDir opens the directory path (named relative to parentFD),
// creating it with mode 0700 when missing. followExisting allows a
// pre-existing entry to be a symlink; a new one is always opened with
// O_NOFOLLOW and fixed up through its descriptor (fixNewMode).
//
// followExisting is set only for the lock parent (~/.cache), which is shared
// with other applications, so an existing one that cannot be opened gets
// advice specific to the failure (unopenableParentError) and never the
// "remove it" remedy reserved for Gonf's own lock directory.
func (s *lockSetup) openOrCreateDir(parentFD int, path string, followExisting bool) (int, error) {
	name := filepath.Base(path)
	created := false
	if err := unix.Mkdirat(parentFD, name, 0o700); err == nil {
		created = true
		s.remember(parentFD, name, unix.AT_REMOVEDIR)
	} else if !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("create %s: %w", path, err)
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
	if created || !followExisting {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := unix.Openat(parentFD, name, flags, 0)
	switch {
	case err != nil && created && errors.Is(err, unix.EACCES):
		return -1, fmt.Errorf("open new directory %s: %w (the umask removed the owner's own permissions; use a umask such as 022 or 077)", path, err)
	case err != nil && created:
		return -1, fmt.Errorf("open new directory %s: %w", path, err)
	case err != nil && followExisting:
		return -1, unopenableParentError(parentFD, name, path, err)
	case err != nil:
		return -1, fmt.Errorf("open %s: %w%s", path, err, lockRemedy(path))
	}
	s.track(fd)
	if created {
		if err := fixNewMode(fd, unix.S_IFDIR, 0o700); err != nil {
			return -1, fmt.Errorf("new directory %s: %w", path, err)
		}
	}
	return fd, nil
}

// openLockFile opens (creating if needed) the lock file path inside the
// verified lock directory dirFD and verifies it. The returned descriptor is
// not tracked: it becomes the lock and belongs to the caller.
func (s *lockSetup) openLockFile(dirFD int, path string) (int, error) {
	name := filepath.Base(path)
	fd, err := unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	created := err == nil
	if created {
		s.remember(dirFD, name, 0)
	} else if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return -1, fmt.Errorf("open crontab lock %s: %w%s", path, err, lockRemedy(path))
	}
	if created {
		// O_CREAT returned a read-write descriptor even if the umask left the
		// new file with fewer permission bits, and fchmod succeeds because
		// this process owns the file (fixNewMode checks that first).
		if err := fixNewMode(fd, unix.S_IFREG, 0o600); err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("new crontab lock %s: %w", path, err)
		}
	}
	if err := verifyLockObject(fd, unix.S_IFREG, 0o600, s.ownerUID); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("unsafe crontab lock %s: %w%s", path, err, lockRemedy(path))
	}
	return fd, nil
}

// fixNewMode sets the exact mode on an object this process just created,
// after fstat confirms the descriptor refers to such an object (right type,
// owned by the effective uid); the umask may have cleared bits.
func fixNewMode(fd int, fileType, perm uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	if uint32(stat.Mode)&unix.S_IFMT != fileType || stat.Uid != uint32(unix.Geteuid()) {
		return fmt.Errorf("is not the object this process created (uid %d, mode %#o)", stat.Uid, stat.Mode)
	}
	if err := unix.Fchmod(fd, perm); err != nil {
		return fmt.Errorf("set mode %#o: %w", perm, err)
	}
	return nil
}

// verifyLockObject checks type, owner and exact mode of an open object.
func verifyLockObject(fd int, fileType, perm, ownerUID uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	switch {
	case uint32(stat.Mode)&unix.S_IFMT != fileType:
		return fmt.Errorf("file type %#o, want %#o", uint32(stat.Mode)&unix.S_IFMT, fileType)
	case stat.Uid != ownerUID:
		return fmt.Errorf("owned by uid %d, want uid %d", stat.Uid, ownerUID)
	case uint32(stat.Mode)&0o7777 != perm:
		return fmt.Errorf("mode %#o, want %#o", uint32(stat.Mode)&0o7777, perm)
	}
	return nil
}

// unopenableParentError explains why the existing lock parent path (entry
// name in parentFD) could not be opened, keeping openErr for errors.Is. The
// parent is ~/.cache, shared with every other application and possibly
// root-owned, so the advice repairs it in place and never suggests removing
// it: see parentAdvice. When the entry cannot even be inspected (e.g. a
// dangling symlink) the plain open error is all there is to report.
func unopenableParentError(parentFD int, name, path string, openErr error) error {
	var stat unix.Stat_t
	if err := statLockParentEntry(parentFD, name, &stat); err != nil {
		return fmt.Errorf("open %s: %w", path, openErr)
	}
	attrs := lockParentAttrs{uid: stat.Uid, gid: stat.Gid, mode: uint32(stat.Mode)}
	ids := lockIDs{euid: uint32(unix.Geteuid()), egid: uint32(unix.Getegid())}
	advice := parentAdvice(path, attrs, ids, openErr)
	if advice == "" {
		return fmt.Errorf("open %s: %w", path, openErr)
	}
	return fmt.Errorf("open %s: %w; %s", path, openErr, advice)
}

// parentAdvice words the fix for an unopenable lock parent. An own parent
// whose mode denies the owner (EACCES with an owner rwx bit missing, e.g.
// mode 0000) needs its owner bits back. An own 0700 parent still denied is
// blocked by something else (SELinux, AppArmor), where chmod would not help,
// so it gets no chmod advice. Everything else reuses checkLockParent's rule
// and wording, so a file in its place reads "is not a directory" and a
// foreign (typically root) owner "chown it back to uid N", exactly as when
// the parent does open. An empty result means there is no specific advice.
func parentAdvice(path string, a lockParentAttrs, ids lockIDs, openErr error) string {
	ownerDenied := a.mode&0o700 != 0o700 && errors.Is(openErr, unix.EACCES)
	if a.uid == ids.euid && a.mode&unix.S_IFMT == unix.S_IFDIR && ownerDenied {
		return fmt.Sprintf("crontab lock parent %s denies its owner uid %d access (mode %#o); chmod u+rwx it", path, ids.euid, a.mode&0o7777)
	}
	if err := checkLockParent(path, a, ids); err != nil {
		return err.Error()
	}
	return ""
}

// lockRemedy is the actionable tail of an error about an existing object of
// Gonf's own: the lock directory or a lock file, never the shared parent
// (see unopenableParentError). They live in the applying account's own
// namespace and hold nothing but locks, so that account can always remove a
// damaged one and Gonf recreates it on the next apply.
func lockRemedy(path string) string {
	return fmt.Sprintf("; remove %s so Gonf can recreate it", path)
}
