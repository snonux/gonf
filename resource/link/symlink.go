package link

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// ensureSymlink ensures l.path is a symlink pointing at l.target.
// It refuses to create or keep a symlink whose target path does not exist
// (dangling / broken links are treated as apply failures).
func ensureSymlink(l *Link) error {
	id := fmt.Sprintf("Symlink[%s]", l.path)
	logger.Debug("processing symlink: %s -> %s", l.path, l.target)

	if l.target == "" {
		return fmt.Errorf("symlink %s has no target", l.path)
	}

	if err := assertSymlinkTargetExists(l.path, l.target); err != nil {
		return err
	}

	info, err := os.Lstat(l.path)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		current, err := os.Readlink(l.path)
		if err != nil {
			return fmt.Errorf("failed to read symlink %s: %w", l.path, err)
		}
		if current == l.target {
			logger.Debug("symlink %s already points at %s", l.path, l.target)
			resource.Note(id, resource.StatusOK)
			return nil
		}
		logger.Debug("repointing symlink %s from %s to %s", l.path, current, l.target)
		if resource.DryRun() {
			resource.Note(id, resource.StatusWouldChange)
			logger.Info("dry-run: would repoint symlink %s", l.path)
			return nil
		}
		if err := os.Remove(l.path); err != nil {
			return fmt.Errorf("failed to remove stale symlink %s: %w", l.path, err)
		}

	case err == nil:
		old := l.path + ".old"
		if resource.DryRun() {
			resource.Note(id, resource.StatusWouldChange)
			logger.Info("dry-run: would replace %s with symlink", l.path)
			return nil
		}
		logger.Debug("%s is a real file/dir, renaming to %s", l.path, old)
		if err := os.Rename(l.path, old); err != nil {
			return fmt.Errorf("failed to move existing %s aside: %w", l.path, err)
		}

	case !os.IsNotExist(err):
		return fmt.Errorf("failed to stat %s: %w", l.path, err)

	default:
		if resource.DryRun() {
			resource.Note(id, resource.StatusWouldChange)
			logger.Info("dry-run: would create symlink %s -> %s", l.path, l.target)
			return nil
		}
	}

	if err := os.Symlink(l.target, l.path); err != nil {
		return fmt.Errorf("failed to create symlink %s -> %s: %w", l.path, l.target, err)
	}

	resource.Note(id, resource.StatusChanged)
	logger.Info("created symlink %s -> %s", l.path, l.target)
	return nil
}

// assertSymlinkTargetExists reports an error if resolving target from linkPath
// would yield a dangling symlink. Relative targets are resolved against the
// link's directory. filepath.Clean is applied so Rex-style trailing slashes
// still resolve when the cleaned path exists.
func assertSymlinkTargetExists(linkPath, target string) error {
	resolved := target
	if !filepath.IsAbs(target) {
		resolved = filepath.Join(filepath.Dir(linkPath), target)
	}
	resolved = filepath.Clean(resolved)

	if _, err := os.Stat(resolved); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("symlink %s: refusing broken link to %q (target does not exist)", linkPath, target)
		}
		return fmt.Errorf("symlink %s: cannot stat target %q: %w", linkPath, target, err)
	}
	return nil
}
