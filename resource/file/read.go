package file

// Safe reads of existing content for line edits and of recipe-declared
// source files: a symlink is followed like os.ReadFile, but the open never
// blocks on or streams from a FIFO, socket, device node or directory, and
// such entries are refused with an error naming their type.

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// entryTypeName renders mode's type bits as a short human name for error
// messages: a policy refusal naming "FIFO" or "directory" reads better
// than os.FileMode's terse type rune ("p", "d").
func entryTypeName(mode os.FileMode) string {
	switch {
	case mode&os.ModeNamedPipe != 0:
		return "FIFO"
	case mode&os.ModeSocket != 0:
		return "unix socket"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return "character device"
	case mode&os.ModeDevice != 0:
		return "block device"
	case mode&os.ModeDir != 0:
		return "directory"
	case mode&os.ModeIrregular != 0:
		return "irregular file"
	default:
		return fmt.Sprintf("entry of type %v", mode.Type())
	}
}

// readFollowNonBlocking reads all bytes from the file at path, following a
// symlink at the final component exactly like the os.ReadFile calls it
// replaces, but so that neither the open nor the read can ever block on or
// stream from a non-regular entry:
//
//   - the open carries O_NONBLOCK, so it returns immediately even when a
//     FIFO sits at path — possible inside the caller's Lstat-to-open
//     window, or when path is a symlink to one;
//   - an fstat of the OPENED entry refuses anything that is not a regular
//     file before a single byte is read: a followed symlink may point at a
//     FIFO (whose read would error EAGAIN with a writer present and, worse,
//     misread as an empty file without one) or at a device node such as
//     /dev/zero, whose reads never end. fstat inspects the opened inode
//     itself, so an entry swapped in between the caller's Lstat and this
//     open cannot slip past the check either;
//   - a regular file reads identically with O_NONBLOCK set.
func readFollowNonBlocking(path string) ([]byte, error) {
	fd, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	defer func() { _ = fd.Close() }()

	if fi, statErr := fd.Stat(); statErr == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("file %s: cannot read from a %s at that path", path, entryTypeName(fi.Mode()))
	}
	raw, err := io.ReadAll(fd)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	return raw, nil
}

// readForLineEdit reads the current content at path for a line edit,
// preserving the former os.ReadFile semantics for everything the line-edit
// path manages:
//
//   - a missing path surfaces its os.ErrNotExist-able error (resolveLine
//     maps it to the noop/create behaviors);
//   - a symlink at the target is followed for the read — the line edit
//     applies to the target's content, and ensureFile then replaces the
//     symlink with the regular managed file, matching its replace rule;
//   - a regular file is read as before.
//
// Any other entry type at the target — planted FIFO, unix socket, device
// node, directory — is a loud user error naming the path and the entry
// type: line edits manage regular files, and unlike the content path there
// is no "replace with new content" semantics to fall back on. The read
// itself never blocks on a planted FIFO (see readFollowNonBlocking).
func readForLineEdit(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("file %s: cannot apply line edits to a %s", path, entryTypeName(info.Mode()))
	}
	return readFollowNonBlocking(path)
}

// readForSource reads the recipe-declared source file at path, preserving
// the former os.ReadFile semantics for regular files and symlinks (a
// symlink source's target is read through). A missing path and any other
// non-regular entry type — planted FIFO, unix socket, device node,
// directory — are loud errors naming the path: the source is content the
// recipe declared, never something gonf may replace, classify as changed,
// or read as empty. The read itself never blocks on a planted FIFO (see
// readFollowNonBlocking). This guards both the single-file WithSource path
// and dir's source-tree copies, which delegate every file with WithSource
// to Ensure (direct path) or EnsureWithPlanFacts (plan path); both go
// through build()/apply() and therefore through this read.
func readForSource(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("cannot read %s: not a regular file (found %s)", path, entryTypeName(info.Mode()))
	}
	return readFollowNonBlocking(path)
}
