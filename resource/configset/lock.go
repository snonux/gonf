package configset

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// lockTimeout bounds how long an apply waits for a concurrent publication,
// and lockPoll is how often it retries meanwhile. They are variables only so
// tests can shorten them; production code never reassigns them.
var (
	lockTimeout = 5 * time.Minute
	lockPoll    = 50 * time.Millisecond
)

// flockFD is flock(2); a variable only so tests can simulate filesystems
// that refuse it. Production code never reassigns it.
var flockFD = unix.Flock

// heldDir is one opened directory and the identity used to order and
// deduplicate the locks.
type heldDir struct {
	path string
	fd   int
	dev  uint64
	ino  uint64
}

// lockDirs takes an exclusive flock(2) on every directory in dirs (the member
// directories) and returns the function releasing them. Every non-dry-run
// config-set apply takes them before it diffs, stages or publishes, so two
// applies whose sets share a directory are serialized for their whole
// read-validate-publish-report sequence, and the pending markers, which live
// in those directories, are never updated concurrently. That holds for two
// gonf processes and for two goroutines of one process alike: a flock belongs
// to the open file description, and each call opens its own.
//
// The directories are opened with internal/safepath (a symlink anywhere in
// the path is refused) and then deduplicated and ordered by (device, inode),
// not by path text. Two spellings of one directory (a bind mount, say) would
// otherwise be locked twice through two descriptors, and the second LOCK_EX
// would never be granted. The fixed (dev, ino) order also gives every apply
// the same acquisition order, which rules out lock-order deadlocks between
// sets with overlapping directories.
//
// A lock held by another publication is waited for, polling, for at most
// lockTimeout (5 minutes); then the apply fails with a timeout error. The
// locks are advisory: plain File resources, package scripts and editors do
// not take them. Locking the directories themselves, rather than lock files
// beside the configuration, leaves nothing behind in /etc.
func lockDirs(dirs []string) (func(), error) {
	held, err := openLockDirs(dirs)
	if err != nil {
		return nil, err
	}
	release := func() {
		for _, h := range held {
			// Closing the descriptor releases its flock.
			_ = unix.Close(h.fd)
		}
	}
	deadline := time.Now().Add(lockTimeout)
	for _, h := range held {
		if err := lockDir(h, deadline); err != nil {
			release()
			return nil, err
		}
	}
	return release, nil
}

// openLockDirs opens every directory without following symlinks and returns
// them sorted by (dev, ino) with duplicates closed and dropped.
func openLockDirs(dirs []string) ([]heldDir, error) {
	var held []heldDir
	closeAll := func() {
		for _, h := range held {
			_ = unix.Close(h.fd)
		}
	}
	for _, dir := range dirs {
		fd, err := openDir(dir, safepath.Walk{})
		if err != nil {
			closeAll()
			return nil, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			_ = unix.Close(fd)
			closeAll()
			return nil, fmt.Errorf("inspect %s: %w", dir, err)
		}
		held = append(held, heldDir{path: dir, fd: fd, dev: uint64(st.Dev), ino: uint64(st.Ino)})
	}
	sort.Slice(held, func(i, j int) bool {
		if held[i].dev != held[j].dev {
			return held[i].dev < held[j].dev
		}
		return held[i].ino < held[j].ino
	})
	unique := held[:0]
	for i, h := range held {
		if i > 0 && h.dev == held[i-1].dev && h.ino == held[i-1].ino {
			_ = unix.Close(h.fd)
			continue
		}
		unique = append(unique, h)
	}
	return unique, nil
}

// lockDir takes h's exclusive lock, polling until deadline when another
// publication holds it.
func lockDir(h heldDir, deadline time.Time) error {
	waiting := false
	for {
		err := flockFD(h.fd, unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EBADF), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EOPNOTSUPP):
			// Linux NFS refuses flock on a directory descriptor with EBADF;
			// ENOTSUP and EOPNOTSUPP are distinct on OpenBSD and NetBSD.
			return fmt.Errorf("lock %s: this filesystem does not support flock(2) on a directory (NFS?); config-set members must live on a local filesystem: %w", h.path, err)
		case errors.Is(err, unix.ENOLCK):
			// No locks available (e.g. the lock table is full): transient,
			// not a property of the filesystem.
			return fmt.Errorf("lock %s: no locks available right now; retry later: %w", h.path, err)
		case !errors.Is(err, unix.EWOULDBLOCK):
			return fmt.Errorf("lock %s: %w", h.path, err)
		}
		if !waiting {
			logger.Info("config set: waiting for a concurrent publication in %s", h.path)
			waiting = true
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lock %s: another config-set publication still holds it after %s; retry later", h.path, lockTimeout)
		}
		time.Sleep(lockPoll)
	}
}
