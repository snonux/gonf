package link

import (
	"fmt"
	"os"

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
// entry is moved aside to path.old, the link is created by create, and the
// aside is removed afterwards so a completed conversion leaves no residue. If
// create fails, the aside is moved back so the original entry is restored at
// path and create's error is returned. If the aside cannot be removed after a
// successful create (e.g. it was a non-empty directory), an error naming the
// backup path is returned; the link itself is in place and the user's data
// remains available under the backup path.
func replaceWithLink(path string, create func() error) error {
	if err := assertNoAsideBackup(path); err != nil {
		return err
	}

	old := asidePath(path)
	logger.Debug("moving existing %s aside to %s", path, old)
	if err := os.Rename(path, old); err != nil {
		return fmt.Errorf("failed to move existing %s aside to %s: %w", path, old, err)
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
