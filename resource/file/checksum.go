package file

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func getChecksum(path string) [32]byte {
	var checksum [32]byte
	data, err := os.ReadFile(path)
	if err != nil {
		logger.Debug("reading %s: %v (file does not exist or cannot be read)", path, err)
		return checksum
	}
	checksum = sha256.Sum256(data)
	logger.Debug("computed checksum for %s: %x", path, checksum)
	return checksum
}

func (f *File) ensureFile(path string, content []byte) error {
	id := fmt.Sprintf("File[%s]", path)
	existingChecksum := getChecksum(path)
	newChecksum := sha256.Sum256(content)
	logger.Debug("computed checksum for new content: %x", newChecksum)
	// Security rule: a file resource never follows or chmods through a
	// symlink at its target path. A symlink sitting at the target counts as
	// changed even when its content matches, so the symlink is replaced by
	// the managed regular file via the atomic write path's rename instead
	// of leaving it in place for attribute application to follow.
	changed := existingChecksum != newChecksum || isSymlink(path)

	if !changed {
		resource.Note(id, resource.StatusOK)
		if resource.DryRun() {
			return nil
		}
		return f.applyAttributesTo(path)
	}

	if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would update %s", path)
		return nil
	}

	if err := atomicWrite(path, content, f.mode); err != nil {
		return err
	}

	resource.Note(id, resource.StatusChanged)
	logger.Info("updated %s", path)
	return f.applyAttributesTo(path)
}

// isSymlink reports whether a symlink sits at path itself. Used to treat a
// planted symlink at a file resource's target path as "needs replacement":
// the symlink is replaced by the managed regular file, never followed.
func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
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
// that might sit at path. The temporary file carries the final mode before
// the rename, so path never briefly exists with a wrong mode; ownership is
// applied afterwards by applyAttributesTo on the final path.
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
	return nil
}
