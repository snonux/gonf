package configset

import (
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/snonux/gonf/internal/validator"
	"github.com/snonux/gonf/resource/file"
)

// system holds the operating-system operations and limits an apply uses
// whose failure paths the tests must reach deterministically: the member
// writes, backups and restores of a publication, the attribute repair, the
// validator runner, the directory locks and the fsync/unlink calls of the
// pending markers. Every apply reaches them through its spec's sys field, never
// through package-level variables, so a test builds its own system with
// newSystem, overrides single fields (to inject a failure, observe a call or
// shorten a timeout) and passes it in; other tests, including parallel ones,
// keep the production defaults. Production code never changes a system after
// newSystem returned it, so one value may be shared by concurrent applies.
type system struct {
	// writeMember replaces a member's live file (publish.go).
	writeMember func(t *file.Target, content []byte) error
	// linkBackup hard-links a member into the backups directory; a failure
	// makes the publication fall back to a copy.
	linkBackup func(dirfd int, name, dst string) error
	// restoreCopy writes a copy backup back to the member path during a
	// rollback.
	restoreCopy func(path string, c *copiedFile) error
	// chownFile and chmodFile set the numeric owner and the mode of a backup
	// copy and of a restored copy, in that order (a chown may clear
	// setuid/setgid, so the chmod must come last).
	chownFile func(f *os.File, uid, gid int) error
	chmodFile func(f *os.File, mode os.FileMode) error
	// applyAttributes re-applies an unchanged member's mode and ownership in
	// place (apply.go).
	applyAttributes func(t *file.Target) error
	// runValidator runs one set validator in dir (stage.go);
	// runValidatorWithheld is its variant for a sensitive set, whose failure
	// never carries the validator output.
	runValidator         func(dir, bin string, args []string) error
	runValidatorWithheld func(dir, bin string, args []string) error
	// flock is flock(2) on a member directory; lockTimeout bounds how long an
	// apply waits for a concurrent publication and lockPoll is how often it
	// retries meanwhile (lock.go).
	flock       func(fd, how int) error
	lockTimeout time.Duration
	lockPoll    time.Duration
	// euid is the applying uid a pending marker must be owned by.
	euid func() int
	// fsyncFD is fsync(2) on an open directory; fsyncDir applies its
	// best-effort policy on top (fsdir.go).
	fsyncFD func(fd int) error
	// syncDirectory makes a rollback's restore durable (fsdir.go), and
	// syncDirFD makes a marker creation or removal durable (marker.go). Both
	// default to fsyncDir, so they honour an overridden fsyncFD.
	syncDirectory func(dir string) error
	syncDirFD     func(fd int, dir string) error
	// unlinkAt is unlinkat(2), used to remove pending markers.
	unlinkAt func(dirfd int, path string, flags int) error
}

// newSystem returns the production operations. The defaults that build on
// other fields (restoreCopy on chownFile/chmodFile, syncDirectory and syncDirFD
// on fsyncFD) are method values of the returned system, so they read those
// fields at call time: a test that overrides chownFile or fsyncFD also changes
// what the defaults built on them do, exactly as the direct calls would.
func newSystem() *system {
	sys := &system{
		writeMember: func(t *file.Target, content []byte) error { return t.Write(content) },
		linkBackup: func(dirfd int, name, dst string) error {
			return unix.Linkat(dirfd, name, unix.AT_FDCWD, dst, 0)
		},
		chownFile:            func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) },
		chmodFile:            func(f *os.File, mode os.FileMode) error { return f.Chmod(mode) },
		applyAttributes:      func(t *file.Target) error { return t.ApplyAttributes() },
		runValidator:         validator.RunIn,
		runValidatorWithheld: validator.RunInWithheld,
		flock:                unix.Flock,
		lockTimeout:          5 * time.Minute,
		lockPoll:             50 * time.Millisecond,
		euid:                 os.Geteuid,
		fsyncFD:              unix.Fsync,
		unlinkAt:             unix.Unlinkat,
	}
	sys.restoreCopy = sys.writeRestoredCopy
	sys.syncDirectory = sys.syncDir
	sys.syncDirFD = sys.fsyncDir
	return sys
}
