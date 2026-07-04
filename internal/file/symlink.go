package file

import (
	"fmt"
	"log"
	"os"
)

// haveSymlink ensures f.path is a symlink pointing at f.symlinkTarget.
//
// Idempotency and clobber policy:
//   - already points at the target: nothing to do.
//   - points elsewhere: the link is removed and recreated.
//   - a real file/dir is in the way: it is renamed to "<path>.old" before the
//     link is created (matching the Rexfile's rename-existing behavior).
func (f *File) haveSymlink() error {
	log.Printf("processing symlink: %s -> %s", f.path, f.symlinkTarget)

	if f.symlinkTarget == "" {
		return fmt.Errorf("symlink %s has no target", f.path)
	}

	info, err := os.Lstat(f.path)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		current, err := os.Readlink(f.path)
		if err != nil {
			return fmt.Errorf("failed to read symlink %s: %w", f.path, err)
		}
		if current == f.symlinkTarget {
			log.Printf("symlink %s already points at %s", f.path, f.symlinkTarget)
			return nil
		}
		log.Printf("repointing symlink %s from %s to %s", f.path, current, f.symlinkTarget)
		if err := os.Remove(f.path); err != nil {
			return fmt.Errorf("failed to remove stale symlink %s: %w", f.path, err)
		}

	case err == nil:
		old := f.path + ".old"
		log.Printf("%s is a real file/dir, renaming to %s", f.path, old)
		if err := os.Rename(f.path, old); err != nil {
			return fmt.Errorf("failed to move existing %s aside: %w", f.path, err)
		}

	case !os.IsNotExist(err):
		return fmt.Errorf("failed to stat %s: %w", f.path, err)
	}

	if err := os.Symlink(f.symlinkTarget, f.path); err != nil {
		return fmt.Errorf("failed to create symlink %s -> %s: %w", f.path, f.symlinkTarget, err)
	}

	log.Printf("created symlink %s -> %s", f.path, f.symlinkTarget)
	return nil
}
