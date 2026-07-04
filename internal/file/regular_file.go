package file

import (
	"crypto/sha256"
	"log"
)

// haveRegularFile writes content to f.path idempotently (via a checksum-guarded
// temp file) and enforces mode/ownership.
func (f *File) haveRegularFile(content []byte) error {
	log.Printf("processing file: %s", f.path)
	existingChecksum := getChecksum(f.path)
	newChecksum := sha256.Sum256(content)
	log.Printf("computed checksum for new content: %x", newChecksum)

	tmpPath := f.path + ".tmp"
	if err := writeTmpFile(tmpPath, content, f.mode); err != nil {
		return err
	}

	if err := updateFromTmp(tmpPath, f.path, existingChecksum != newChecksum); err != nil {
		return err
	}

	return f.applyAttributes()
}
