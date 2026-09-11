package file

import (
	"crypto/sha256"
	"fmt"
	"os"

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
	changed := existingChecksum != newChecksum

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

	tmpPath := path + ".tmp"
	if err := writeTmpFile(tmpPath, content, f.mode); err != nil {
		return err
	}

	if err := updateFromTmp(tmpPath, path, true); err != nil {
		return err
	}

	resource.Note(id, resource.StatusChanged)
	logger.Info("updated %s", path)
	return f.applyAttributesTo(path)
}

func writeTmpFile(tmpPath string, content []byte, mode os.FileMode) error {
	logger.Debug("writing %d bytes to temporary file %s with mode %v", len(content), tmpPath, mode)
	if err := os.WriteFile(tmpPath, content, mode); err != nil {
		logger.Debug("failed to write temporary file %s: %v", tmpPath, err)
		return err
	}
	logger.Debug("successfully wrote temporary file %s", tmpPath)
	return nil
}

func updateFromTmp(tmpPath, path string, checksumChanged bool) error {
	if !checksumChanged {
		logger.Debug("checksums match, removing temporary file %s", tmpPath)
		if err := os.Remove(tmpPath); err != nil {
			return err
		}
		return nil
	}

	logger.Debug("checksums differ, renaming %s to %s", tmpPath, path)
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
