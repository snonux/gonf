package file

// Reconciliation for the two modes that do not write managed content:
// removing an absent file (IsAbsent) and EnsureFile's create-if-missing that
// preserves existing content. Content writes live in checksum.go
// (ensureFile) and validation.go (ensureValidatedFile).

import (
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func ensureAbsent(path string) error {
	return ensureAbsentWithID(path, resource.FormatID("File", path))
}

func ensureAbsentWithID(path, id string) error {
	logger.Debug("ensuring absent: %s", path)

	if _, err := os.Lstat(path); os.IsNotExist(err) {
		logger.Debug("%s already absent", path)
		resource.Note(id, resource.StatusOK)
		return nil
	}

	if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would remove %s", path)
		return nil
	}

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			resource.Note(id, resource.StatusOK)
			return nil
		}
		return fmt.Errorf("failed to remove %s: %w", path, err)
	}

	resource.Note(id, resource.StatusChanged)
	logger.Info("removed %s", path)
	return nil
}

// ensurePresent creates an empty regular file when path is absent. Existing
// regular files retain their content; only explicitly requested attributes
// are reconciled. This is the apply-side behavior of api.EnsureFile.
func (f *File) ensurePresent() error {
	path := f.targetPath()
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return f.ensureFile(path, []byte{})
	}
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("file %s: cannot ensure a %s while preserving content", path, entryTypeName(info.Mode()))
	}

	id := f.reportID(path)
	changed, err := f.explicitMetadataChanged(info)
	if err != nil {
		return err
	}
	if !changed {
		resource.Note(id, resource.StatusOK)
		return nil
	}
	if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		return nil
	}

	attrs := *f
	if !attrs.modeSet {
		attrs.mode = info.Mode()
	}
	if !attrs.userSet {
		attrs.user = ""
	}
	if !attrs.groupSet {
		attrs.group = ""
	}
	if err := attrs.applyAttributesTo(path); err != nil {
		return err
	}
	resource.Note(id, resource.StatusChanged)
	return nil
}
