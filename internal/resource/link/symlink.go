package link

import (
	"fmt"
	"log"
	"os"
)

// ensureSymlink ensures l.path is a symlink pointing at l.target.
//
// Idempotency and clobber policy:
//   - already points at the target: nothing to do.
//   - points elsewhere: the link is removed and recreated.
//   - a real file/dir is in the way: it is renamed to "<path>.old" before the
//     link is created (matching the Rexfile's rename-existing behavior).
func ensureSymlink(l *Link) error {
	log.Printf("processing symlink: %s -> %s", l.path, l.target)

	if l.target == "" {
		return fmt.Errorf("symlink %s has no target", l.path)
	}

	info, err := os.Lstat(l.path)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		current, err := os.Readlink(l.path)
		if err != nil {
			return fmt.Errorf("failed to read symlink %s: %w", l.path, err)
		}
		if current == l.target {
			log.Printf("symlink %s already points at %s", l.path, l.target)
			return nil
		}
		log.Printf("repointing symlink %s from %s to %s", l.path, current, l.target)
		if err := os.Remove(l.path); err != nil {
			return fmt.Errorf("failed to remove stale symlink %s: %w", l.path, err)
		}

	case err == nil:
		old := l.path + ".old"
		log.Printf("%s is a real file/dir, renaming to %s", l.path, old)
		if err := os.Rename(l.path, old); err != nil {
			return fmt.Errorf("failed to move existing %s aside: %w", l.path, err)
		}

	case !os.IsNotExist(err):
		return fmt.Errorf("failed to stat %s: %w", l.path, err)
	}

	if err := os.Symlink(l.target, l.path); err != nil {
		return fmt.Errorf("failed to create symlink %s -> %s: %w", l.path, l.target, err)
	}

	log.Printf("created symlink %s -> %s", l.path, l.target)
	return nil
}
