package link

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// ensureHardlink ensures l.path is a hard link to l.target.
//
// Idempotency and clobber policy:
//   - already the same inode as the target: nothing to do.
//   - a different file/link is in the way: it is renamed to "<path>.old"
//     before the link is created (matching the symlink resource's behavior).
func ensureHardlink(l *Link) error {
	log.Printf("processing hardlink: %s -> %s", l.path, l.target)

	if l.target == "" {
		return fmt.Errorf("hardlink %s has no target", l.path)
	}

	targetInfo, err := os.Stat(l.target)
	if err != nil {
		return fmt.Errorf("failed to stat hardlink target %s: %w", l.target, err)
	}

	if info, err := os.Lstat(l.path); err == nil {
		if sameInode(info, targetInfo) {
			log.Printf("hardlink %s already links to %s", l.path, l.target)
			return nil
		}
		old := l.path + ".old"
		log.Printf("%s already exists, renaming to %s", l.path, old)
		if err := os.Rename(l.path, old); err != nil {
			return fmt.Errorf("failed to move existing %s aside: %w", l.path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat %s: %w", l.path, err)
	}

	if err := os.Link(l.target, l.path); err != nil {
		return fmt.Errorf("failed to create hardlink %s -> %s: %w", l.path, l.target, err)
	}

	log.Printf("created hardlink %s -> %s", l.path, l.target)
	return nil
}

// sameInode reports whether two FileInfos refer to the same underlying inode
// (same device and inode number), i.e. they are already hard-linked.
func sameInode(a, b os.FileInfo) bool {
	as, aok := a.Sys().(*syscall.Stat_t)
	bs, bok := b.Sys().(*syscall.Stat_t)
	if !aok || !bok {
		return false
	}
	return as.Dev == bs.Dev && as.Ino == bs.Ino
}
