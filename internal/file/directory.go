package file

import (
	"fmt"
	"log"
	"os"
)

// haveDirectory ensures f.path exists as a directory with the desired mode and
// ownership. It is idempotent: an existing directory only has its attributes
// re-enforced, and an existing non-directory is an error.
func (f *File) haveDirectory() error {
	log.Printf("processing directory: %s", f.path)

	info, err := os.Lstat(f.path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", f.path)
		}
		log.Printf("directory %s already exists", f.path)

	case os.IsNotExist(err):
		log.Printf("creating directory %s with mode %v", f.path, f.mode)
		if err := os.MkdirAll(f.path, f.mode); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", f.path, err)
		}

	default:
		return fmt.Errorf("failed to stat %s: %w", f.path, err)
	}

	return f.applyAttributes()
}

// haveAbsent removes f.path if it exists. It is idempotent: a missing path is
// not an error. By default non-empty directories are not removed; combine with
// PruneDirectory() to remove a directory and its contents recursively.
func (f *File) haveAbsent() error {
	log.Printf("ensuring absent: %s", f.path)

	remove := os.Remove
	if f.pruneDirectory {
		remove = os.RemoveAll
	}

	if err := remove(f.path); err != nil {
		if os.IsNotExist(err) {
			log.Printf("%s already absent", f.path)
			return nil
		}
		return fmt.Errorf("failed to remove %s: %w", f.path, err)
	}

	log.Printf("removed %s", f.path)
	return nil
}
