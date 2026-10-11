package dir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/sourceexclude"
	"github.com/snonux/gonf/resource"
)

func (d *Dir) excluded(rel string) bool {
	return sourceexclude.Matches(d.exclude, filepath.ToSlash(rel))
}

// pruneTreeExcluded prunes bottom-up, preserving excluded entries and their
// ancestors. Removing a stale parent recursively before visiting its excluded
// children would delete the very paths the recipe leaves unmanaged.
func pruneTreeExcluded(d *Dir) error {
	if resource.DryRun() {
		if _, err := os.Lstat(d.path); os.IsNotExist(err) {
			return nil
		}
	}
	var stale []string
	protected := make(map[string]bool)
	err := filepath.WalkDir(d.path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(d.path, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.protected(rel) {
			for parent := filepath.Dir(rel); parent != "."; parent = filepath.Dir(parent) {
				protected[parent] = true
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceEntryExists(d.source, rel) {
			stale = append(stale, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(stale) - 1; i >= 0; i-- {
		rel := stale[i]
		if protected[rel] {
			continue
		}
		path := filepath.Join(d.path, rel)
		if resource.DryRun() {
			resource.Note(resource.FormatID("File", path), resource.StatusWouldChange)
			logger.Info("dry-run: would prune %s", path)
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("failed to prune %s: %w", path, err)
		}
		resource.Note(resource.FormatID("File", path), resource.StatusChanged)
		logger.Info("pruned %s", path)
	}
	return nil
}

// protected also checks the source spelling of a rendered template.
func (d *Dir) protected(rel string) bool { return d.excluded(rel) || d.excluded(rel+".tmpl") }
