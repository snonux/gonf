package plan

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/snonux/gonf/internal/safepath"
)

// This file writes tree and glob blobs relative to the descriptor of the
// verified blobs/ directory (Store.writeEntries), never by path: every entry
// is removed, created or opened below a held directory descriptor, and no
// path component is followed if it is a symlink. The layout is the one the
// path-based writer made before: directories exactly 0700, files 0600 (masked
// by the umask, like os.WriteFile), symlinks with their raw target.

// dirReadFlags opens a directory readable, so removeAllAt can list it,
// without following a symlink in the component opened.
const dirReadFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// treeFileFlags creates or truncates a blob file without following a symlink
// planted in its place. O_TRUNC rather than O_EXCL: a flat glob blob may list
// the same basename twice (matches from different directories), and the last
// one wins, as it did with os.WriteFile.
const treeFileFlags = unix.O_WRONLY | unix.O_CREAT | unix.O_TRUNC | unix.O_NOFOLLOW | unix.O_CLOEXEC

// removeAllAt removes name below parent and, when it is a directory,
// everything in it, like os.RemoveAll but relative to parent and without
// following a symlink: a symlink (to a directory or not) is unlinked itself,
// never descended into. A missing name is not an error. rel is name's path in
// errors (relative to the plan directory, "blobs/<ref>/..."), which are
// *os.PathErrors carrying the real cause, as os.RemoveAll's were.
func removeAllAt(parent int, name, rel string) error {
	err := unix.Unlinkat(parent, name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	// unlink(2) refuses a directory (EISDIR on Linux, EPERM on the BSDs and
	// macOS). For anything else (a file or symlink we may not remove) the
	// unlink error is the cause.
	if !isDirAt(parent, name) {
		return &os.PathError{Op: "unlinkat", Path: rel, Err: err}
	}
	// An empty directory goes with rmdir alone, even one we may not list
	// (os.RemoveAll does the same); a non-empty one is emptied first.
	if err := unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	fd, err := unix.Openat(parent, name, dirReadFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return &os.PathError{Op: "open", Path: rel, Err: err}
	}
	if err := removeChildrenAt(fd, rel); err != nil {
		return err
	}
	if err := unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return &os.PathError{Op: "unlinkat", Path: rel, Err: err}
	}
	return nil
}

// isDirAt reports whether name below parent is a directory (not following a
// symlink). A name that cannot be examined counts as not a directory.
func isDirAt(parent int, name string) bool {
	var st unix.Stat_t
	return unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR
}

// removeChildrenAt removes every entry of the open directory fd (whose path
// in errors is rel) through removeAllAt, and closes fd.
func removeChildrenAt(fd int, rel string) error {
	dir := os.NewFile(uintptr(fd), rel)
	defer func() { _ = dir.Close() }()
	children, err := dir.Readdirnames(-1)
	if err != nil {
		// Readdirnames names the directory by the *os.File's name, which is
		// rel already; keep only the cause so the path is not doubled.
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return &os.PathError{Op: "readdirent", Path: rel, Err: err}
	}
	for _, child := range children {
		if err := removeAllAt(fd, child, rel+"/"+child); err != nil {
			return err
		}
	}
	return nil
}

// createTreeRootAt creates the tree blob directory name below blobsFD
// (exactly 0700) and returns a descriptor of it. removeAllAt has just removed
// it, so finding one already there means someone else made it in between
// (only the owner of blobs/ can, as blobs/ passed the directory rule): that
// directory is refused rather than filled, since it may hold anything.
func createTreeRootAt(blobsFD int, name string) (int, error) {
	fd, created, err := safepath.OpenOrCreateDirAt(blobsFD, name, nil)
	if err != nil {
		return -1, err
	}
	if !created {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("%s reappeared after it was cleared", name)
	}
	return fd, nil
}

// materializeAt writes manifest entries below the open tree directory
// treeFD: directories exactly 0700, files by content (0600), symlinks raw
// (exactly what the source tree carries, dangling included). Parent
// directories are created as needed, though sorted manifests list parents
// before children. Errors name the entry's Rel.
func materializeAt(treeFD int, entries []BlobEntry) error {
	for _, e := range entries {
		if err := materializeEntryAt(treeFD, e); err != nil {
			return fmt.Errorf("%s: %w", e.Rel, err)
		}
	}
	return nil
}

// materializeEntryAt writes the one entry e below treeFD.
func materializeEntryAt(treeFD int, e BlobEntry) error {
	parts, err := entryParts(e.Rel)
	if err != nil {
		return err
	}
	dirs, leaf := parts[:len(parts)-1], parts[len(parts)-1]
	if e.Kind == BlobDir {
		dirs, leaf = parts, ""
	}
	return withDirAt(treeFD, dirs, func(dirFD int) error {
		switch e.Kind {
		case BlobDir:
			return nil
		case BlobSymlink:
			return unix.Symlinkat(e.Target, dirFD, leaf)
		default:
			return writeTreeFileAt(dirFD, leaf, e.Data)
		}
	})
}

// entryParts splits a manifest Rel into its components. Rels come from
// scanTree/scanGlob and never climb out of the tree; one that would (a ".."
// component) or that names nothing is refused rather than walked.
func entryParts(rel string) ([]string, error) {
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, fmt.Errorf("invalid blob entry path %q", rel)
		}
	}
	return parts, nil
}

// withDirAt opens (creating exactly 0700 when missing, never following a
// symlink) the directories dirs below treeFD and calls fn with the
// descriptor of the last one, or with treeFD itself when dirs is empty.
func withDirAt(treeFD int, dirs []string, fn func(dirFD int) error) error {
	if len(dirs) == 0 {
		return fn(treeFD)
	}
	fd, err := safepath.Walk{Create: true}.OpenAt(treeFD, ".", dirs)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return fn(fd)
}

// writeTreeFileAt writes data as the file name (0600) below dirFD, without
// following a symlink in its place.
func writeTreeFileAt(dirFD int, name string, data []byte) error {
	fd, err := unix.Openat(dirFD, name, treeFileFlags, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
