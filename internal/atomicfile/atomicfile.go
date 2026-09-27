// Package atomicfile replaces a file durably and atomically: a private
// temporary file beside the target, fsync, rename over the target, fsync of
// the parent directory. It is the one such writer in gonf; every resource
// that rewrites a whole file in place (resource/file's File and Target, the
// NetBSD rc.conf and rc.conf.d edits in resource/service) uses it rather
// than restating the steps, so none of them can forget one of the syncs.
package atomicfile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"

	"github.com/snonux/gonf/internal/logger"
)

// Owner is a numeric file ownership. A -1 id leaves that id as the
// writer's, like os.Chown's -1.
type Owner struct {
	UID, GID int
}

// OwnerOf returns the ownership recorded in info, as returned by os.Stat or
// os.Lstat; ok is false when info carries none (a synthetic FileInfo).
func OwnerOf(info fs.FileInfo) (owner Owner, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Owner{}, false
	}
	return Owner{UID: int(st.Uid), GID: int(st.Gid)}, true
}

// beforeRename holds the ObserveBeforeRenameForTest observer, if any.
var beforeRename atomic.Pointer[func(tmpPath string)]

// ObserveBeforeRenameForTest is a test seam: until the returned restore
// func runs, every Write calls seen with its finished temporary file's path
// right before the rename, so a test can check what the temporary file
// carries (content, mode, ownership) before it becomes visible at the
// target. It swaps package state, so tests using it must not run in
// parallel with other writes they do not expect to observe.
func ObserveBeforeRenameForTest(seen func(tmpPath string)) (restore func()) {
	old := beforeRename.Swap(&seen)
	return func() { beforeRename.Store(old) }
}

// Option adjusts one Write.
type Option func(*options)

type options struct {
	owner *Owner
}

// WithOwner gives the temporary file owner before the rename (and before
// its mode, so a set-id bit is never set on a file the wrong user owns), so
// path never briefly exists with the wrong ownership. A chown the caller may
// not make (changing the owner without privilege) fails the write and
// leaves path untouched; one that changes nothing is skipped.
func WithOwner(owner Owner) Option {
	return func(o *options) { o.owner = &owner }
}

// tmpNamePattern builds the os.CreateTemp pattern for path: the target's
// base name plus the ".gonftmp" marker and a random-suffix placeholder.
// CreateTemp's random suffix can be up to 10 bytes, so the fixed part is
// capped to keep the full name within NAME_MAX (255) even for very long
// base names that themselves fit on disk.
func tmpNamePattern(path string) string {
	const (
		marker  = ".gonftmp"
		maxRand = 10
		nameMax = 255
	)
	base := filepath.Base(path)
	if max := nameMax - len(marker) - maxRand; len(base) > max {
		base = base[:max]
	}
	return base + marker + "*"
}

// Write installs content at path via a temporary file and an atomic rename.
// The temporary file is created with os.CreateTemp in the target's own
// directory using O_CREATE|O_EXCL and an unpredictable random name, so
// neither a pre-planted symlink at a predictable location (e.g. path+".tmp")
// nor a competing writer can redirect the write: the name cannot be guessed
// in advance and an existing file can never be opened through the create.
// The rename replaces path as a directory entry and never follows a symlink
// that might sit at path; it replaces ANY entry type atomically and safely
// (regular file, symlink, FIFO, socket, device node — no open of the planted
// entry involved), except a directory, which rename cannot replace and
// reports as an error. A caller that means to edit the file a symlink
// points to resolves the link first and passes the target. The temporary
// file carries the final mode (and, with WithOwner, the final ownership)
// before the rename, so path never briefly exists with wrong attributes.
// Without WithOwner it is owned by the writer (resource/file applies its
// configured ownership to the final path afterwards).
//
// Durability: the temporary file is fsynced before the rename so its data
// and attributes are on stable storage when path first appears, and the
// parent directory is fsynced after the rename so the directory entry swap
// itself survives a crash. Without these syncs a power loss can surface path
// as zero-length or leave the pre-rename (stale) content behind. The
// directory sync is best-effort: a failure is logged at debug level and does
// not fail the write, because some filesystems refuse directory fsync —
// durability of the rename must not gate delivering the configuration.
//
// On any error before the rename, path is left untouched and the temporary
// file is removed.
func Write(path string, content []byte, mode os.FileMode, opts ...Option) (err error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tmpNamePattern(path))
	if err != nil {
		return fmt.Errorf("failed to create temporary file next to %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	logger.Debug("created temporary file %s", tmpPath)
	defer func() {
		if err != nil {
			// Best-effort cleanup of the temporary file; after a successful
			// rename it no longer exists and err is nil anyway.
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err = tmp.Write(content); err != nil {
		return fmt.Errorf("failed to write temporary file %s: %w", tmpPath, err)
	}
	if err = chownTemp(tmp, o.owner); err != nil {
		return fmt.Errorf("failed to chown temporary file %s for %s: %w", tmpPath, path, err)
	}
	// Chmod after the chown: a chown may clear set-id bits.
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("failed to chmod temporary file %s to %v: %w", tmpPath, mode, err)
	}
	// Push the content and attributes to stable storage before the rename
	// makes them visible at path; otherwise a crash right after the rename
	// can leave a zero-length or partially written target.
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary file %s: %w", tmpPath, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file %s: %w", tmpPath, err)
	}

	if seen := beforeRename.Load(); seen != nil {
		(*seen)(tmpPath)
	}
	logger.Debug("renaming %s to %s", tmpPath, path)
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to move temporary file %s into place at %s: %w", tmpPath, path, err)
	}
	syncDir(filepath.Dir(path), path)
	return nil
}

// chownTemp gives tmp owner, skipping a chown that would change nothing (so
// an unprivileged caller keeping its own ownership never needs a chown it
// might not be allowed to make).
func chownTemp(tmp *os.File, owner *Owner) error {
	if owner == nil {
		return nil
	}
	info, err := tmp.Stat()
	if err != nil {
		return err
	}
	if cur, ok := OwnerOf(info); ok && cur == owner.resolve(cur) {
		return nil
	}
	return tmp.Chown(owner.UID, owner.GID)
}

// resolve returns o with each -1 id replaced by cur's.
func (o Owner) resolve(cur Owner) Owner {
	if o.UID == -1 {
		o.UID = cur.UID
	}
	if o.GID == -1 {
		o.GID = cur.GID
	}
	return o
}

// syncDir makes the rename to path durable by syncing its parent directory
// dir. Best-effort: it logs and continues on failure, so exotic filesystems
// that refuse directory fsync never block the apply.
func syncDir(dir, path string) {
	d, err := os.Open(dir)
	if err == nil {
		if err = d.Sync(); err == nil {
			logger.Debug("synced directory %s to make the rename durable", dir)
		}
		_ = d.Close()
	}
	if err != nil {
		logger.Debug("fsync of directory %s after rename to %s: %v (best-effort, continuing)", dir, path, err)
	}
}
