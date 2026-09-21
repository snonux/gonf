package configset

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource/file"
)

// publication replaces the changed members of a validated set. Several
// renames are not an atomic transaction, so it provides bounded rollback
// instead: before the first live write every member about to be replaced is
// backed up into the private staging directory, and when any replacement
// fails, all members replaced so far (including the failing one, whose rename
// may already have happened) are restored in reverse order. A crash or kill
// between two renames cannot be rolled back; the pending marker each member
// gets right before its rename makes the next apply report it as changed
// while it converges forward (see marker.go and docs/config-set.md).
type publication struct {
	set     *spec
	stage   *stage
	targets []*file.Target
	live    [][]byte
	// pending are the members that already had a marker before this
	// publication; a rollback keeps their markers.
	pending map[string]bool
	// keepStage is set when a rollback failed: the staging directory then
	// holds the only copy of some replaced files and must not be removed.
	keepStage bool
}

// backup is how one member can be put back. A member that did not exist is
// restored by removing it again. An existing one is normally preserved as a
// hard link to its old inode (restoring renames the link back, which brings
// back the exact bytes, mode, ownership and every other inode attribute). If
// the staging directory is on another filesystem, or the link is refused, a
// copy with the same bytes, permission bits and numeric owner is written to
// the same backups/ path instead, so the on-disk backup exists either way.
type backup struct {
	index   int
	existed bool
	path    string // backups/<n>, a hard link or a copy
	copied  *copiedFile
}

// copiedFile is the copy fallback of a backup; the same bytes and attributes
// are also on disk at backup.path.
type copiedFile struct {
	data     []byte
	mode     os.FileMode
	uid, gid int
}

// writeMember, linkBackup and restoreCopyFile are the live-write, backup and
// copy-restore primitives of a publication. They are variables solely so
// tests can inject a failure after some members were replaced (to exercise
// rollback), refuse hard links (to exercise the copy fallback) or fail a
// restore; production code never reassigns them.
var (
	writeMember = func(t *file.Target, content []byte) error { return t.Write(content) }
	linkBackup  = func(dirfd int, name, dst string) error {
		return unix.Linkat(dirfd, name, unix.AT_FDCWD, dst, 0)
	}
	restoreCopyFile = restoreCopy
	// chownFile and chmodFile set the numeric owner and the mode of a backup
	// copy and of a restored copy, in that order (a chown may clear
	// setuid/setgid, so the chmod must come last). Tests record their calls
	// to pin both the carried-over values and the order.
	chownFile = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
	chmodFile = func(f *os.File, mode os.FileMode) error { return f.Chmod(mode) }
)

// run backs up the changed members and then, in declaration order, creates
// each member's pending marker and replaces it.
func (p *publication) run(changed map[string]bool) error {
	var order []int
	for i, m := range p.set.members {
		if changed[m.key] {
			order = append(order, i)
		}
	}
	backups, err := p.takeBackups(order)
	if err != nil {
		return fmt.Errorf("config set %s: back up live members, nothing published: %w", p.set.name, err)
	}
	for n, i := range order {
		m := p.set.members[i]
		if err := createMarker(p.set.name, m); err != nil {
			return p.rollback(backups[:n], m.key, err)
		}
		if err := writeMember(p.targets[i], p.live[i]); err != nil {
			return p.rollback(backups[:n+1], m.key, err)
		}
	}
	return nil
}

// takeBackups records how to restore each member in order. It only creates
// entries inside the private staging directory (backups/ is 0700), never at
// a live path.
func (p *publication) takeBackups(order []int) ([]backup, error) {
	if err := os.Mkdir(p.stage.backups, 0o700); err != nil {
		return nil, err
	}
	backups := make([]backup, 0, len(order))
	for n, i := range order {
		b, err := p.backupMember(i, filepath.Join(p.stage.backups, strconv.Itoa(n)))
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", p.set.members[i].key, err)
		}
		backups = append(backups, b)
	}
	return backups, nil
}

// backupMember preserves member i's live file at dst. The member is opened
// through internal/safepath (no symlink anywhere in its path, never blocking
// on a FIFO, regular files only) and everything after that works on what was
// opened: the hard link is made relative to the checked directory descriptor,
// and the copy fallback reads the opened file and its fstat attributes. A
// FIFO or symlink swapped in after the diff is refused, never followed.
func (p *publication) backupMember(i int, dst string) (backup, error) {
	path := p.set.members[i].path
	dirfd, f, err := openMember(path)
	if errors.Is(err, unix.ENOENT) {
		return backup{index: i}, nil
	}
	if err != nil {
		return backup{}, fmt.Errorf("%w: no longer a regular file", err)
	}
	defer func() {
		_ = unix.Close(dirfd)
		_ = f.Close()
	}()
	if err := linkBackup(dirfd, filepath.Base(path), dst); err == nil {
		return backup{index: i, existed: true, path: dst}, nil
	}
	copied, err := copyForBackup(f)
	if err != nil {
		return backup{}, err
	}
	if err := writeBackupCopy(dst, copied); err != nil {
		return backup{}, fmt.Errorf("write backup copy of %s: %w", path, err)
	}
	return backup{index: i, existed: true, path: dst, copied: copied}, nil
}

// copyForBackup reads the bytes and attributes of the opened member that the
// copy fallback restores.
func copyForBackup(f *os.File) (*copiedFile, error) {
	info, err := safepath.Fstat(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return &copiedFile{data: data, mode: unixModeToGo(info.Perm()), uid: int(info.UID), gid: int(info.GID)}, nil
}

// unixModeToGo converts chmod bits (with setuid, setgid, sticky) to an
// os.FileMode.
func unixModeToGo(perm uint32) os.FileMode {
	mode := os.FileMode(perm & 0o777)
	if perm&unix.S_ISUID != 0 {
		mode |= os.ModeSetuid
	}
	if perm&unix.S_ISGID != 0 {
		mode |= os.ModeSetgid
	}
	if perm&unix.S_ISVTX != 0 {
		mode |= os.ModeSticky
	}
	return mode
}

// writeBackupCopy writes the copy fallback to dst: created exclusively and
// without following links (0600 while being written), then given the
// original owner and mode and fsynced, so an operator can restore it by hand.
func writeBackupCopy(dst string, c *copiedFile) (err error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := f.Write(c.data); err != nil {
		return err
	}
	if err := chownFile(f, c.uid, c.gid); err != nil {
		return err
	}
	if err := chmodFile(f, c.mode); err != nil {
		return err
	}
	return f.Sync()
}

// rollback restores backups in reverse order after member failed with cause.
// Each member is restored, its directory fsynced, and only then is its
// pending marker removed (unless it was pending before this publication), so
// a power loss can never keep a replaced-away new file whose marker is gone.
// The returned error always names the failed member. When a restore (or its
// fsync) fails, the error lists the members left in an unknown state, their
// markers stay so the next apply signals them, and the staging directory with
// the backups is kept for manual recovery.
func (p *publication) rollback(backups []backup, failed string, cause error) error {
	var problems, leftovers []string
	for j := len(backups) - 1; j >= 0; j-- {
		m := p.set.members[backups[j].index]
		var stale *staleMarkerError
		switch err := p.undo(backups[j]); {
		case errors.As(err, &stale):
			leftovers = append(leftovers, stale.err.Error())
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s (%s): %v", m.key, m.path, err))
		}
	}
	if len(problems) == 0 {
		logger.Warn("config set %s: rolled back %d member(s) after publishing %s failed", p.set.name, len(backups), failed)
		note := ""
		if len(leftovers) > 0 {
			note = fmt.Sprintf("; pending markers could not be removed: %s", strings.Join(leftovers, "; "))
		}
		return fmt.Errorf("config set %s: publishing member %s failed: %w; rolled back %d member(s), the previous live files are restored%s",
			p.set.name, failed, cause, len(backups), note)
	}
	p.keepStage = true
	return fmt.Errorf("config set %s: publishing member %s failed: %w; ROLLBACK INCOMPLETE for %s; remaining backups kept in %s",
		p.set.name, failed, cause, strings.Join(problems, "; "), p.stage.backups)
}

// staleMarkerError reports that a member was restored durably but its pending
// marker could not be removed. That is not a failed rollback: the live file is
// known, and the marker causes at most one extra signal (see
// markerRemovalError for when it is certain and when it only may happen).
type staleMarkerError struct{ err error }

func (e *staleMarkerError) Error() string { return e.err.Error() }

// undo restores one member, makes the restore durable, then drops the marker
// this publication created for it.
func (p *publication) undo(b backup) error {
	m := p.set.members[b.index]
	if err := p.restore(b); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(m.path)); err != nil {
		return fmt.Errorf("make the restore durable: %w", err)
	}
	if p.pending[m.key] {
		return nil
	}
	if err := removeMarker(p.set.name, m); err != nil {
		return &staleMarkerError{err: err}
	}
	return nil
}

// restore puts one member back to its pre-publication state.
func (p *publication) restore(b backup) error {
	path := p.set.members[b.index].path
	switch {
	case !b.existed:
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	case b.copied == nil:
		return os.Rename(b.path, path)
	default:
		return restoreCopyFile(path, b.copied)
	}
}

// restoreCopy atomically writes a copied backup back: private temp file
// beside path, bytes, numeric owner, mode (after chown, which may clear
// setuid/setgid), fsync, rename.
func restoreCopy(path string, c *copiedFile) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".gonfrestore*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(c.data); err != nil {
		return err
	}
	if err = chownFile(tmp, c.uid, c.gid); err != nil {
		return err
	}
	if err = chmodFile(tmp, c.mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
