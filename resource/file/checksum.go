package file

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// getChecksum returns the sha256 of the file at path, or the zero checksum
// when path is missing or unreadable (the caller compares against the new
// content and typically rewrites). It must only be called on regular files
// or known-missing paths: its read-open carries no O_NONBLOCK, so it would
// block indefinitely on a non-regular entry such as a planted FIFO —
// ensureFile classifies the target with os.Lstat first and never routes a
// non-regular entry here.
func getChecksum(path string) [32]byte {
	var checksum [32]byte
	data, err := os.ReadFile(path)
	if err != nil {
		logger.Debug("reading %s: %v (file does not exist or cannot be read)", path, err)
		return checksum
	}
	checksum = sha256.Sum256(data)
	// The digest itself is never logged: a file may hold a low-entropy
	// secret, and its unsalted sha256 would let anyone with the debug log
	// confirm guesses offline.
	logger.Debug("computed checksum for %s", path)
	return checksum
}

func (f *File) ensureFile(path string, content []byte) error {
	id := f.reportID(path)
	// Not logged, for the reason given in getChecksum.
	newChecksum := sha256.Sum256(content)

	// Classify the target via Lstat BEFORE any open: os.ReadFile opens
	// without O_NONBLOCK, so a checksum read through a planted FIFO would
	// block the apply indefinitely (until a writer appears). Anything that
	// is not a regular file is counted as changed below and replaced by the
	// managed regular file via the atomic write path's rename, which swaps
	// the directory entry without opening the planted entry.
	needsReplace, entryType := nonRegularEntryAt(path)
	var existingChecksum [32]byte
	if !needsReplace {
		// Regular files open and read safely, and a missing path yields the
		// zero checksum (getChecksum tolerates the failed read).
		existingChecksum = getChecksum(path)
	} else {
		logger.Debug("%s holds a non-regular entry (%v), not a managed file", path, entryType)
	}
	// Security rule: a file resource never follows, reads, or chmods
	// through a non-regular entry at its target path. Any such entry —
	// symlink, FIFO, socket, or device node — counts as changed even when a
	// content comparison might match, so it is replaced by the managed
	// regular file via the atomic write path's rename instead of being left
	// in place for the checksum read or attribute application to touch.
	changed := existingChecksum != newChecksum || needsReplace

	if !changed {
		resource.Note(id, resource.StatusOK)
		if resource.DryRun() {
			return nil
		}
		return f.applyAttributesTo(path)
	}

	return resource.Mutate(id, fmt.Sprintf("update %s", path), func() error {
		if err := atomicWrite(path, content, f.mode); err != nil {
			return err
		}
		logger.Info("updated %s", path)
		return f.applyAttributesTo(path)
	})
}

// nonRegularEntryAt reports whether an entry that is not a managed regular
// file — a symlink, FIFO, socket, device node, or directory — sits at path
// itself, alongside its entry type for logging. Generalizes the former
// isSymlink rule (symlinks count as needing replacement) to every
// non-regular entry type: the atomic write's rename replaces the directory
// entry wholesale, without opening or following the planted entry.
func nonRegularEntryAt(path string) (needsReplace bool, entryType os.FileMode) {
	info, err := os.Lstat(path)
	if err != nil {
		// Missing (or unstatable): no entry to replace; the checksum read
		// below fails the same way it always did, yielding the zero checksum.
		return false, 0
	}
	return !info.Mode().IsRegular(), info.Mode().Type()
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

// atomicWrite installs content at path via a temporary file and an atomic
// rename. The temporary file is created with os.CreateTemp in the target's
// own directory using O_CREATE|O_EXCL and an unpredictable random name, so
// neither a pre-planted symlink at a predictable location (e.g. path+".tmp")
// nor a competing writer can redirect the write: the name cannot be guessed
// in advance and an existing file can never be opened through the create.
// The rename replaces path as a directory entry and never follows a symlink
// that might sit at path; it replaces ANY entry type atomically and safely
// (regular file, symlink, FIFO, socket, device node — no open of the planted
// entry involved), except a directory, which rename cannot replace and
// reports as an error. The temporary file carries the final mode before
// the rename, so path never briefly exists with a wrong mode; ownership is
// applied afterwards by applyAttributesTo on the final path.
//
// Durability: the temporary file is fsynced before the rename so its data is
// on stable storage when path first appears, and the parent directory is
// fsynced after the rename so the directory entry swap itself survives a
// crash. Without these syncs a power loss can surface path as zero-length or
// leave the pre-rename (stale) content behind. The directory sync is
// best-effort: a failure is logged at debug level and does not fail the
// apply, because some filesystems refuse directory fsync (and on Windows
// os.Open of a directory fails outright) — durability of the rename must not
// gate delivering the configuration.
func atomicWrite(path string, content []byte, mode os.FileMode) error {
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
	// Push the content to stable storage before the rename makes it visible
	// at path; otherwise a crash right after the rename can leave a
	// zero-length or partially written target.
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary file %s: %w", tmpPath, err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("failed to chmod temporary file %s to %v: %w", tmpPath, mode, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file %s: %w", tmpPath, err)
	}

	logger.Debug("renaming %s to %s", tmpPath, path)
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to move temporary file %s into place at %s: %w", tmpPath, path, err)
	}

	// Make the rename itself durable by syncing the parent directory.
	// Best-effort: log and continue on failure, so exotic filesystems that
	// refuse directory fsync never block the apply (err must stay nil here —
	// the rename already succeeded and the deferred cleanup looks at it).
	dir, derr := os.Open(filepath.Dir(path))
	if derr == nil {
		if derr = dir.Sync(); derr == nil {
			logger.Debug("synced directory %s to make the rename durable", filepath.Dir(path))
		}
		_ = dir.Close()
	}
	if derr != nil {
		logger.Debug("fsync of directory %s after rename to %s: %v (best-effort, continuing)", filepath.Dir(path), path, derr)
	}
	return nil
}
