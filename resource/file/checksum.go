package file

import (
	"crypto/sha256"
	"log"
	"os"
)

func getChecksum(path string) [32]byte {
	var checksum [32]byte
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("reading %s: %v (file does not exist or cannot be read)", path, err)
		return checksum
	}
	checksum = sha256.Sum256(data)
	log.Printf("computed checksum for %s: %x", path, checksum)
	return checksum
}

func (f *File) ensureFile(path string, content []byte) error {
	existingChecksum := getChecksum(path)
	newChecksum := sha256.Sum256(content)
	log.Printf("computed checksum for new content: %x", newChecksum)

	tmpPath := path + ".tmp"
	if err := writeTmpFile(tmpPath, content, f.mode); err != nil {
		return err
	}

	if err := updateFromTmp(tmpPath, path, existingChecksum != newChecksum); err != nil {
		return err
	}

	return f.applyAttributesTo(path)
}

func writeTmpFile(tmpPath string, content []byte, mode os.FileMode) error {
	log.Printf("writing %d bytes to temporary file %s with mode %v", len(content), tmpPath, mode)
	if err := os.WriteFile(tmpPath, content, mode); err != nil {
		log.Printf("failed to write temporary file %s: %v", tmpPath, err)
		return err
	}
	log.Printf("successfully wrote temporary file %s", tmpPath)
	return nil
}

func updateFromTmp(tmpPath, path string, checksumChanged bool) error {
	if !checksumChanged {
		log.Printf("checksums match, removing temporary file %s", tmpPath)
		if err := os.Remove(tmpPath); err != nil {
			log.Printf("failed to remove temporary file %s: %v", tmpPath, err)
			return err
		}
		log.Printf("no changes needed for %s", path)
		return nil
	}

	log.Printf("checksums differ, renaming %s to %s", tmpPath, path)
	if err := os.Rename(tmpPath, path); err != nil {
		log.Printf("failed to rename %s to %s: %v", tmpPath, path, err)
		os.Remove(tmpPath)
		return err
	}
	log.Printf("successfully updated %s", path)
	return nil
}
