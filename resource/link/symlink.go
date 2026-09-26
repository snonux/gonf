package link

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// ensureSymlink ensures l.path is a symlink pointing at l.target.
// It refuses to create or keep a symlink whose target path does not exist
// (dangling / broken links are treated as apply failures).
func ensureSymlink(l *Link) error {
	id := resource.FormatID("Symlink", l.path)
	logger.Debug("processing symlink: %s -> %s", l.path, l.target)

	if l.target == "" {
		return fmt.Errorf("symlink %s has no target", l.path)
	}

	if err := assertSymlinkTargetExists(l.path, l.target); err != nil {
		// A dry run applies nothing, so a target that an earlier resource
		// of the same plan creates (a Dir, a File, a Package) is still
		// missing here: preview the link instead of failing the whole dry
		// run. The real apply still refuses a dangling link.
		// Every branch below only notes and logs under dry-run.
		if !resource.DryRun() || !errors.Is(err, errTargetMissing) {
			return err
		}
		logger.Info("dry-run: symlink %s target %q does not exist yet; the apply refuses it unless an earlier resource creates it", l.path, l.target)
	}

	info, err := os.Lstat(l.path)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return repointSymlink(l, id)

	case err == nil:
		return replaceExistingSymlink(l, id)

	case !os.IsNotExist(err):
		return fmt.Errorf("failed to stat %s: %w", l.path, err)

	default:
		if resource.DryRun() {
			resource.Note(id, resource.StatusWouldChange)
			logger.Info("dry-run: would create symlink %s -> %s", l.path, l.target)
			return nil
		}
	}

	return createSymlink(l, id)
}

func repointSymlink(l *Link, id string) error {
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
	return createSymlink(l, id)
}

func replaceExistingSymlink(l *Link, id string) error {
	// The assert also runs on dry-runs: the real apply would refuse, so the
	// preview must show it.
	if err := assertNoAsideBackup(l.path); err != nil {
		return err
	}
	if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would replace %s with symlink", l.path)
		return nil
	}
	if err := replaceWithLink(l.path, func() error {
		if err := os.Symlink(l.target, l.path); err != nil {
			return fmt.Errorf("failed to create symlink %s -> %s: %w", l.path, l.target, err)
		}
		return nil
	}); err != nil {
		return err
	}
	resource.Note(id, resource.StatusChanged)
	logger.Info("replaced %s with symlink -> %s", l.path, l.target)
	return nil
}

func createSymlink(l *Link, id string) error {
	if err := os.Symlink(l.target, l.path); err != nil {
		return fmt.Errorf("failed to create symlink %s -> %s: %w", l.path, l.target, err)
	}
	resource.Note(id, resource.StatusChanged)
	logger.Info("created symlink %s -> %s", l.path, l.target)
	return nil
}

// errTargetMissing marks assertSymlinkTargetExists's "target does not exist"
// refusal, which a dry run downgrades to would-change.
var errTargetMissing = errors.New("target does not exist")

// assertSymlinkTargetExists reports an error if resolving target from linkPath
// would yield a dangling symlink. Relative targets are resolved against the
// link's directory the way the kernel resolves them: the link's directory
// and target are concatenated and handed to os.Stat unchanged, never
// cleaned lexically, because filepath.Clean would fold a ".." against the
// path's own spelling while the kernel follows it from wherever a symlinked
// parent actually points (lnk -> real/sub, target "../t" is real/t, not t).
// Trailing slashes of target are trimmed so Rex-style "dir/" and "file/"
// targets still resolve when the entry exists.
func assertSymlinkTargetExists(linkPath, target string) error {
	resolved := strings.TrimRight(target, "/")
	if resolved == "" {
		resolved = "/" // target is the filesystem root
	}
	if !filepath.IsAbs(target) {
		resolved = filepath.Dir(linkPath) + string(filepath.Separator) + resolved
	}

	if _, err := os.Stat(resolved); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("symlink %s: refusing broken link to %q (%w)", linkPath, target, errTargetMissing)
		}
		return fmt.Errorf("symlink %s: cannot stat target %q: %w", linkPath, target, err)
	}
	return nil
}
