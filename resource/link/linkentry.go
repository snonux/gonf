package link

import (
	"os"

	"golang.org/x/sys/unix"
)

// linkEntry creates old as a hard link to the directory entry at path
// itself, never to what a symlink there points to. os.Link uses link(2),
// which follows a symlink on FreeBSD (Linux does not), so the aside of a
// symlink would silently become a copy of its target's data there; linkat
// without AT_SYMLINK_FOLLOW links the entry on every supported GOOS.
func linkEntry(path, old string) error {
	if err := unix.Linkat(unix.AT_FDCWD, path, unix.AT_FDCWD, old, 0); err != nil {
		return &os.LinkError{Op: "link", Old: path, New: old, Err: err}
	}
	return nil
}
