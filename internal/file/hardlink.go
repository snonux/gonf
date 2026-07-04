package file

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// haveHardlink ensures f.path is a hard link to f.hardlinkTarget.
//
// Idempotency and clobber policy:
//   - already the same inode as the target: nothing to do.
//   - a different file/link is in the way: it is renamed to "<path>.old"
//     before the link is created (matching the symlink resource's behavior).
func (f *File) haveHardlink() error {
	log.Printf("processing hardlink: %s -> %s", f.path, f.hardlinkTarget)

	if f.hardlinkTarget == "" {
		return fmt.Errorf("hardlink %s has no target", f.path)
	}

	targetInfo, err := os.Stat(f.hardlinkTarget)
	if err != nil {
		return fmt.Errorf("failed to stat hardlink target %s: %w", f.hardlinkTarget, err)
	}

	if info, err := os.Lstat(f.path); err == nil {
		if sameInode(info, targetInfo) {
			log.Printf("hardlink %s already links to %s", f.path, f.hardlinkTarget)
			return nil
		}
		old := f.path + ".old"
		log.Printf("%s already exists, renaming to %s", f.path, old)
		if err := os.Rename(f.path, old); err != nil {
			return fmt.Errorf("failed to move existing %s aside: %w", f.path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat %s: %w", f.path, err)
	}

	if err := os.Link(f.hardlinkTarget, f.path); err != nil {
		return fmt.Errorf("failed to create hardlink %s -> %s: %w", f.path, f.hardlinkTarget, err)
	}

	log.Printf("created hardlink %s -> %s", f.path, f.hardlinkTarget)
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
