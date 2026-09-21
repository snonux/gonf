package configset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// readableDirFlags reopens a walked directory for the operations a traversal
// descriptor (O_PATH on Linux) does not support: flock(2) and fsync(2).
const readableDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC

// euid is the applying uid used by the ownership check of pending markers.
// It is a variable only so tests can simulate a foreign owner; production
// code never reassigns it.
var euid = os.Geteuid

// openDir walks the clean absolute path from "/" with internal/safepath (no
// component is ever resolved through a symlink) and returns a readable
// descriptor of the final directory, which the caller closes. walk carries
// the caller's policy (Create, Check). Reopening "." relative to the walked
// descriptor names the very same directory, so the guarantee carries over.
func openDir(path string, walk safepath.Walk) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, fmt.Errorf("open %s: not a clean absolute path", path)
	}
	base, parts := safepath.Split(path)
	fd, err := walk.Open(base, parts)
	if err != nil {
		return -1, describeWalkError(path, err)
	}
	defer func() { _ = unix.Close(fd) }()
	rfd, err := unix.Openat(fd, ".", readableDirFlags, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", path, err)
	}
	return rfd, nil
}

// describeWalkError words a walk failure; a missing component keeps
// unix.ENOENT in the chain so callers can treat "absent" as a state.
func describeWalkError(path string, err error) error {
	var ce *safepath.ComponentError
	switch {
	case !errors.As(err, &ce):
		return err
	case errors.Is(ce.Err, safepath.ErrSymlink), errors.Is(ce.Err, unix.ENOTDIR):
		return fmt.Errorf("open %s: %s is not a real directory (symlink or file in the path)", path, ce.Path)
	}
	return fmt.Errorf("open %s: %w", path, ce)
}

// openMember opens the member file path as a regular file without following
// a symlink (anywhere in the path) and without blocking on a planted FIFO. It
// also returns the traversal descriptor of the member's directory, so the
// caller can link the file by name relative to the directory it checked. A
// missing member returns an error wrapping unix.ENOENT (dirfd is then -1).
// The caller closes both.
func openMember(path string) (dirfd int, f *os.File, err error) {
	dir, name := filepath.Dir(path), filepath.Base(path)
	base, parts := safepath.Split(dir)
	dirfd, err = safepath.Walk{}.Open(base, parts)
	if err != nil {
		return -1, nil, describeWalkError(dir, err)
	}
	f, err = safepath.OpenRegularAt(dirfd, name, path)
	if err != nil {
		_ = unix.Close(dirfd)
		if errors.Is(err, unix.ENOENT) {
			return -1, nil, err
		}
		return -1, nil, fmt.Errorf("%s: %w", path, err)
	}
	return dirfd, f, nil
}

// fsyncFD is fsync(2); a variable only so tests can simulate a filesystem
// that refuses directory fsync. Production code never reassigns it.
var fsyncFD = unix.Fsync

// fsyncDir fsyncs an open directory for the marker and restore handling. It
// is stricter than the File resource's atomic write, which ignores every
// directory fsync error: only a filesystem that does not support directory
// fsync at all (FUSE, 9p and similar report EINVAL, ENOTSUP or EOPNOTSUPP;
// the last two differ on OpenBSD and NetBSD) is treated as best-effort,
// because such a filesystem has no durability to offer and failing would only
// make the set alternate between failing and half-succeeding. Every other
// error (EIO, ...) is returned. The member rename's own directory fsync is
// done by the File write path (atomicWrite) and follows its ignore-all rule.
func fsyncDir(fd int, dir string) error {
	err := fsyncFD(fd)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EOPNOTSUPP):
		logger.Debug("fsync of directory %s: %v (not supported here, best-effort, continuing)", dir, err)
		return nil
	}
	return fmt.Errorf("sync %s: %w", dir, err)
}

// syncDirectory fsyncs a directory so renames, links and unlinks inside it
// are durable (best-effort as in fsyncDir). It is a variable only so tests
// can observe which directories a rollback made durable; production code
// never reassigns it.
var syncDirectory = func(dir string) error {
	fd, err := openDir(dir, safepath.Walk{})
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return fsyncDir(fd, dir)
}
