package plan

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/snonux/gonf/internal/logger"
)

// applyRunTTL bounds how long a staging run directory may live before the
// sweep removes it. It is deliberately generous: a legitimate "gonf apply"
// never runs anywhere near this long, so only leftovers from killed or
// crashed applies ever reach the age. It is a variable so tests can shrink
// it (or negate it).
var applyRunTTL = 24 * time.Hour

// runDirPrefix matches the run directories created by NewApplyRunDir.
const runDirPrefix = "run-"

// ApplyStagingRoot returns $TMPDIR/gonf-apply/<uid> (owner-only) under a
// shared, world-writable + sticky root. The shared root may be created by
// different users — pushes run as the SSH login user while elevated chunks
// apply via doas/sudo as root — so it is created (and, when owned, chmod'd)
// like /tmp itself: without this, a root-created root would lock out later
// unprivileged applies on mixed-privilege hosts.
//
// The sticky bit does not stop the shared root's owner from renaming or
// replacing entries, so the shared root is only used when it is a real
// directory owned by root or by the current user; otherwise (e.g. created by
// the SSH login user, and this is root's elevated apply) the per-uid root is
// $TMPDIR/gonf-apply-<uid> directly under $TMPDIR instead. Either way the
// per-uid root must be a real directory (not a symlink) owned by the current
// user, and it is chmod'ed without following a symlink.
func ApplyStagingRoot() (string, error) {
	uid := strconv.Itoa(os.Getuid())
	if u, err := user.Current(); err == nil && u.Uid != "" {
		uid = u.Uid
	}
	shared := filepath.Join(os.TempDir(), "gonf-apply")
	if err := os.Mkdir(shared, 0o777); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("plan apply staging: %w", err)
	}
	root := filepath.Join(shared, uid)
	if ownedDir(shared, true) {
		// Best effort: the shared parent must stay writable for every uid
		// that may apply here. Failures are expected on pre-existing dirs
		// the current user does not own.
		//
		// The sticky bit must be set via os.ModeSticky, not the raw octal
		// literal 0o1777: os.Chmod's Unix path reads the ModeSetuid/
		// ModeSetgid/ModeSticky FileMode flag bits (each a high bit, e.g.
		// ModeSticky is 1<<20) off the mode value, not the low-order 0o1000
		// octal bit a plain integer literal sets. Passing 0o1777 therefore
		// silently drops the sticky bit and leaves the shared root
		// world-writable without it, letting any local user rename another
		// uid's per-uid subdirectory out of the way (task ee2).
		_ = chmodDirNoFollow(shared, os.ModeSticky|0o777)
	} else {
		root = filepath.Join(os.TempDir(), "gonf-apply-"+uid)
	}
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("plan apply staging: %w", err)
	}
	if !ownedDir(root, false) {
		return "", fmt.Errorf("plan apply staging: %s is not a directory owned by the current user (pre-planted?)", root)
	}
	if err := chmodDirNoFollow(root, 0o700); err != nil {
		return "", fmt.Errorf("plan apply staging chmod: %w", err)
	}
	return root, nil
}

// ownedDir reports whether path is a real directory (not a symlink) owned by
// the effective user, or also by root when rootOK is set.
func ownedDir(path string, rootOK bool) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	euid := uint32(os.Geteuid())
	return st.Uid == euid || rootOK && st.Uid == 0
}

// chmodDirNoFollow sets the mode of the directory path through a handle
// opened without following a symlink at path.
func chmodDirNoFollow(path string, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(mode)
}

// SweepApplyStaging removes run directories under root whose modification
// time is older than applyRunTTL (leftovers from killed or crashed applies).
// Fresh directories are never touched, so the sweep is safe to run while
// other applies are in flight. Entries that are not run directories are left
// alone. Per-entry failures do not stop the sweep; the joined error is
// best-effort and callers must treat it as such (see NewApplyRunDir).
func SweepApplyStaging(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("plan apply sweep %s: %w", root, err)
	}
	now := time.Now()
	var errs []error
	for _, e := range entries {
		if !staleRunDir(e, now) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("plan apply sweep %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// staleRunDir reports whether the entry is a run directory old enough to be
// swept. Anything that is not a run directory (other names, plain files,
// symlinks) is left alone, as are entries whose age cannot be determined.
// "sealed-run-..." entries never match: they have their own prefix and their
// own sweep (sweepSealedApplyRuns), so the two never race each other's
// directories.
func staleRunDir(e os.DirEntry, now time.Time) bool {
	if !e.IsDir() || !strings.HasPrefix(e.Name(), runDirPrefix) {
		return false
	}
	return olderThanApplyTTL(e, now)
}

// olderThanApplyTTL reports whether e's modification time is older than
// applyRunTTL, or false when its age cannot be determined (vanished mid-sweep
// or unreadable — left to the next sweep either way).
func olderThanApplyTTL(e os.DirEntry, now time.Time) bool {
	info, err := e.Info()
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) > applyRunTTL
}

// NewApplyRunDir creates a fresh 0700 run directory for one apply, after a
// best-effort sweep of stale leftovers. Concurrent applies each get their own
// directory and remove it again via the returned cleanup, so they never
// interfere with each other. A sweep failure is logged at debug level and
// ignored: the garbage collection must never break a new apply.
func NewApplyRunDir() (dir string, cleanup func(), err error) {
	root, err := ApplyStagingRoot()
	if err != nil {
		return "", nil, err
	}
	if err := SweepApplyStaging(root); err != nil {
		logger.Debug("plan apply staging sweep (ignored): %v", err)
	}
	dir, err = os.MkdirTemp(root, runDirPrefix+"*")
	if err != nil {
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, cleanup, nil
}

// sealedRunDirPrefix matches the run directories NewSealedApplyRunDir
// creates: "sealed-run-<owning pid>-<random>". Distinct from runDirPrefix
// ("run-", which "sealed-run-..." does not share as a prefix) so an ordinary
// push/apply run dir and a sealed apply's run dir are never touched by the
// other's sweep.
const sealedRunDirPrefix = "sealed-run-"

// NewSealedApplyRunDir creates a fresh 0700 run directory for one sealed
// apply's blob extraction (docs/design/plan-encryption.md, "Plaintext after
// decryption"), named sealed-run-<pid>-* so a leftover from a killed or
// crashed sealed apply can be identified by its owning PID. Before creating
// the new directory it sweeps this uid's staging root: every sealed-run-*
// entry whose recorded PID is no longer alive is removed regardless of its
// age, and one whose PID is still alive — or has since been reused by an
// unrelated process, the acknowledged residual risk noted below — falls
// back to the ordinary applyRunTTL age rule instead of being kept
// indefinitely. This is tighter than the plain run-* sweep
// (SweepApplyStaging, 24h lazily): a sealed apply's run dir holds plaintext
// specifically because its plan.age was sealed for confidentiality at rest,
// so leaving a dead apply's leftover for up to 24h would defeat much of the
// point of sealing it in the first place.
//
// Residual risk: PID reuse. Between a killed sealed apply's process exiting
// and this sweep running, the OS can hand the same PID to an unrelated
// process; sweepSealedApplyRuns cannot tell that case apart from the
// original apply somehow still running, so it treats the PID as alive and
// leaves the leftover to the ordinary 24h rule instead of removing it
// immediately. This is the same class of residual risk the design doc
// documents rather than hides (docs/design/plan-encryption.md, "Plaintext after
// decryption": "a leftover until the next sealed apply of that uid").
func NewSealedApplyRunDir() (dir string, cleanup func(), err error) {
	root, err := ApplyStagingRoot()
	if err != nil {
		return "", nil, err
	}
	if err := sweepSealedApplyRuns(root); err != nil {
		logger.Debug("plan sealed apply staging sweep (ignored): %v", err)
	}
	dir, err = os.MkdirTemp(root, sealedRunDirPrefix+strconv.Itoa(os.Getpid())+"-*")
	if err != nil {
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, cleanup, nil
}

// sweepSealedApplyRuns removes sealed-run-* entries under root: immediately
// when the PID encoded in their name is no longer alive (regardless of age),
// and otherwise — a live or PID-reused entry — only once it is older than
// applyRunTTL, the same threshold a plain run-* directory needs
// (staleRunDir). An entry whose name does not parse as
// "sealed-run-<positive integer>-..." was not created by
// NewSealedApplyRunDir and is left alone rather than guessed at. Per-entry
// failures do not stop the sweep; the joined error is best-effort, like
// SweepApplyStaging's.
func sweepSealedApplyRuns(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("plan sealed apply sweep %s: %w", root, err)
	}
	now := time.Now()
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, ok := sealedRunDirPID(e.Name())
		if !ok {
			continue
		}
		if processAlive(pid) && !olderThanApplyTTL(e, now) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("plan sealed apply sweep %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// sealedRunDirPID parses the owning PID out of a sealed-run-* directory
// name ("sealed-run-<pid>-<random>"). ok is false for anything that does not
// have the prefix or whose PID field is not a positive integer, so a
// foreign or malformed name is never guessed at.
func sealedRunDirPID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, sealedRunDirPrefix)
	if !ok {
		return 0, false
	}
	field, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(field)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive reports whether pid names a process this uid can still see:
// signal 0 (no actual signal sent) only checks for existence and permission
// (Kill(2)/kill(2) on every unix gonf targets: linux, freebsd, openbsd,
// netbsd, darwin). ESRCH means the process is gone; any other outcome
// (success, or an error such as EPERM meaning it exists but is not ours to
// signal) is read as "alive" — the conservative direction for a sweep that
// deletes files, since sweepSealedApplyRuns' age fallback still bounds how
// long a mistaken "alive" leftover survives.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}
