package file

import (
	"crypto/sha256"
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/atomicfile"
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
		if err := atomicfile.Write(path, content, f.mode); err != nil {
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
