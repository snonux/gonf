package link

import (
	"fmt"
	"os"
	"syscall"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// ensureHardlink ensures l.path is a hard link to l.target.
func ensureHardlink(l *Link) error {
	id := fmt.Sprintf("Hardlink[%s]", l.path)
	logger.Debug("processing hardlink: %s -> %s", l.path, l.target)

	if l.target == "" {
		return fmt.Errorf("hardlink %s has no target", l.path)
	}

	targetInfo, err := os.Stat(l.target)
	if err != nil {
		return fmt.Errorf("failed to stat hardlink target %s: %w", l.target, err)
	}

	if info, err := os.Lstat(l.path); err == nil {
		if sameInode(info, targetInfo) {
			logger.Debug("hardlink %s already links to %s", l.path, l.target)
			resource.Note(id, resource.StatusOK)
			return nil
		}
		// The assert also runs on dry-runs: the real apply would refuse,
		// so the preview must show it.
		if err := assertNoAsideBackup(l.path); err != nil {
			return err
		}
		if resource.DryRun() {
			resource.Note(id, resource.StatusWouldChange)
			logger.Info("dry-run: would replace %s with hardlink", l.path)
			return nil
		}
		if err := replaceWithLink(l.path, func() error {
			if err := os.Link(l.target, l.path); err != nil {
				return fmt.Errorf("failed to create hardlink %s -> %s: %w", l.path, l.target, err)
			}
			return nil
		}); err != nil {
			return err
		}
		resource.Note(id, resource.StatusChanged)
		logger.Info("replaced %s with hardlink -> %s", l.path, l.target)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat %s: %w", l.path, err)
	} else if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would create hardlink %s -> %s", l.path, l.target)
		return nil
	}

	if err := os.Link(l.target, l.path); err != nil {
		return fmt.Errorf("failed to create hardlink %s -> %s: %w", l.path, l.target, err)
	}

	resource.Note(id, resource.StatusChanged)
	logger.Info("created hardlink %s -> %s", l.path, l.target)
	return nil
}

func sameInode(a, b os.FileInfo) bool {
	as, aok := a.Sys().(*syscall.Stat_t)
	bs, bok := b.Sys().(*syscall.Stat_t)
	if !aok || !bok {
		return false
	}
	return as.Dev == bs.Dev && as.Ino == bs.Ino
}
