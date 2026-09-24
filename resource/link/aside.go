package link

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/snonux/gonf/internal/logger"
)

// asideSuffix is appended to a link's path while the real entry sitting there
// is moved aside during conversion into a symlink or hardlink.
const asideSuffix = ".old"

// asidePath returns the transient backup path used while converting the entry
// at path into a link.
func asidePath(path string) string {
	return path + asideSuffix
}

// assertNoAsideBackup refuses conversion when any entry already exists at the
// aside path. Moving the existing entry aside would silently overwrite
// whatever the user keeps there, so the conversion is refused and the
// conflict must be resolved manually. Lstat is used, so every entry type —
// file, directory, symlink (even a dangling one) — triggers the refusal.
// The check is Lstat-then-act and therefore alone no guarantee against an
// entry planted between the check and the move; the kernel-enforced
// no-replace move in moveAsideNoReplace is the backstop for that race. The
// assert's job is the friendly refusal for backups that already exist.
func assertNoAsideBackup(path string) error {
	old := asidePath(path)
	if _, err := os.Lstat(old); err == nil {
		return fmt.Errorf("link %s: refusing to overwrite existing backup %s; remove or rename it first", path, old)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("link %s: failed to check for existing backup %s: %w", path, old, err)
	}
	return nil
}

// replaceWithLink converts the real entry at path into a link. The existing
// entry is moved aside to path.old (without ever replacing an entry planted
// at path.old, see moveAsideNoReplace), the link is created by create, and
// the aside is removed afterwards so a completed conversion leaves no
// residue. If create fails, the aside is moved back so the original entry is
// restored at path and create's error is returned. If the aside cannot be
// removed after a successful create (e.g. it was a non-empty directory), an
// error naming the backup path is returned; the link itself is in place and
// the user's data remains available under the backup path.
func replaceWithLink(path string, create func() error) error {
	if err := assertNoAsideBackup(path); err != nil {
		return err
	}

	old := asidePath(path)
	logger.Debug("moving existing %s aside to %s", path, old)
	if err := moveAsideNoReplace(path, old); err != nil {
		return err
	}

	if err := create(); err != nil {
		if rerr := os.Rename(old, path); rerr != nil {
			return fmt.Errorf("%w (additionally, restoring the original %s from %s failed: %v)",
				err, path, old, rerr)
		}
		logger.Debug("restored original %s after failed link creation", path)
		return err
	}

	logger.Debug("removing aside %s after successful conversion", old)
	if err := os.Remove(old); err != nil {
		return fmt.Errorf("created link %s but could not remove the backup %s; remove it manually: %w",
			path, old, err)
	}
	return nil
}

// moveAsideNoReplace moves the entry at path to the aside path old without
// ever replacing an entry that appears at old after the caller's pre-check.
//
// For non-directories on non-Darwin platforms the move is built from
// link(2), whose EEXIST check against the aside is atomic: os.Link fails
// immediately instead of overwriting when something is planted at old
// between the check and the move. The original name is removed only after
// the aside link exists; for the brief window in between the data is
// reachable under both names, and an interrupted move leaves both entries
// (the next apply then refuses on the leftover backup instead of destroying
// data).
//
// link(2) cannot hardlink directories, and on Darwin it follows symlinks —
// the aside would capture the target's data instead of the entry itself —
// so directories and all Darwin entries fall back to os.Rename. Rename
// overwrites the destination: a non-empty directory planted at old makes
// the move fail loudly (ENOTEMPTY), while an empty planted one is silently
// lost. That is the accepted residual race for directories and darwin; see
// docs/design/file-dir-link.md.
func moveAsideNoReplace(path, old string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("failed to move existing %s aside to %s: %w", path, old, err)
	}

	if !info.IsDir() && runtime.GOOS != "darwin" {
		return moveAsideHardlink(path, old)
	}

	if err := os.Rename(path, old); err != nil {
		return fmt.Errorf("failed to move existing %s aside to %s: %w", path, old, err)
	}
	return nil
}

// moveAsideHardlink moves a non-directory entry aside by hardlinking it to
// old (an atomic no-replace creation: link(2) fails with EEXIST when an
// entry is already there) and then removing the original name. Both steps
// preserve the inode: the data lives on under old until replaceWithLink
// removes it after a successful conversion, and rollback (rename old back
// to path) keeps it either way.
func moveAsideHardlink(path, old string) error {
	if err := os.Link(path, old); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("existing backup %s appeared during conversion (pre-planted?): %w", old, err)
		}
		return fmt.Errorf("failed to move existing %s aside to %s: %w", path, old, err)
	}

	if err := os.Remove(path); err != nil {
		// The original is still at path; drop the just-created aside link so
		// the state matches the pre-move state instead of leaving a backup
		// that would block every later apply.
		if rerr := os.Remove(old); rerr != nil {
			return fmt.Errorf("failed to move existing %s aside to %s: removing %s failed: %w (additionally, removing the fresh backup %s failed: %v)",
				path, old, path, err, old, rerr)
		}
		return fmt.Errorf("failed to move existing %s aside to %s: %w", path, old, err)
	}
	return nil
}
