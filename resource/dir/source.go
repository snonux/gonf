package dir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
)

// copySourceTree mirrors d.source into d.path, dispatching each entry by
// kind. Symlink-ness is checked before the dir/file branches: fs.DirEntry
// reports a symlink's own type via Lstat semantics (never following it), so
// a symlink in the source tree is recreated as a symlink rather than read as
// file content.
func copySourceTree(d *Dir) error {
	logger.Debug("installing files from source %s to %s", d.source, d.path)

	return filepath.WalkDir(d.source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(d.source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		target := filepath.Join(d.path, rel)

		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			return copySourceSymlink(path, target)
		case entry.IsDir():
			return copySourceDir(d, target)
		default:
			return copySourceFile(d, path, target)
		}
	})
}

func copySourceDir(d *Dir, target string) error {
	if err := os.MkdirAll(target, d.mode); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", target, err)
	}
	return applyAttributesTo(target, d.mode, d.user, d.group)
}

// copySourceSymlink recreates the symlink found at sourcePath as a symlink
// at target, preserving its raw (unresolved) link target string. This is
// correct as long as the destination tree mirrors the source tree 1:1; an
// absolute link target pointing back into the source tree itself is not
// remapped into the destination — a pre-existing conceptual limitation of
// copying a tree of symlinks.
func copySourceSymlink(sourcePath, target string) error {
	rawTarget, err := os.Readlink(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read symlink %s: %w", sourcePath, err)
	}
	return link.Ensure(target, opt.WithSymlink(rawTarget))
}

// copySourceFile delegates writing a single copied file to the file
// package's own primitive, using d's file-mode default (not d's directory
// mode) and passing the mechanically-derived target path verbatim — file's
// own resolve() strips a ".tmpl" suffix and computes .Param consistently, so
// dir needs no special-casing of its own.
func copySourceFile(d *Dir, sourcePath, target string) error {
	return file.Ensure(target,
		opt.WithSource(sourcePath),
		opt.WithMode(d.fileMode),
		opt.WithOwner(d.user),
		opt.WithGroup(d.group),
	)
}

// pruneTree removes anything under d.path that has no counterpart in
// d.source. A destination entry also counts as having a counterpart if
// d.source has the same relative path with a ".tmpl" suffix appended, since
// copySourceFile (via file.Ensure) strips that suffix when writing —
// otherwise every templated file would be pruned immediately after being
// copied. In dry-run mode nothing is removed; every would-be-pruned path is
// only noted as StatusWouldChange (the walk still descends into stale
// directories so their contents are previewed too).
func pruneTree(d *Dir) error {
	logger.Debug("pruning destination directory %s", d.path)

	return filepath.WalkDir(d.path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(d.path, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil // don't prune the root itself
		}

		if sourceEntryExists(d.source, rel) {
			return nil
		}

		if resource.DryRun() {
			resource.Note(fmt.Sprintf("File[%s]", path), resource.StatusWouldChange)
			logger.Info("dry-run: would prune %s", path)
			return nil // keep walking: nothing may be removed
		}

		logger.Debug("pruning %s", path)
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("failed to prune %s: %w", path, err)
		}
		if entry.IsDir() {
			return filepath.SkipDir // already removed
		}
		return nil
	})
}

func sourceEntryExists(source, rel string) bool {
	if _, err := os.Lstat(filepath.Join(source, rel)); err == nil {
		return true
	}
	_, err := os.Lstat(filepath.Join(source, rel) + ".tmpl")
	return err == nil
}

// copySourceGlob installs regular files matching d.sourceGlob into d.path as
// basename entries (flat, Rex ensure_dir style).
func copySourceGlob(d *Dir) error {
	matches, err := filepath.Glob(d.sourceGlob)
	if err != nil {
		return fmt.Errorf("invalid source glob %q: %w", d.sourceGlob, err)
	}
	logger.Debug("installing glob %s (%d matches) into %s", d.sourceGlob, len(matches), d.path)

	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			return fmt.Errorf("stat glob match %s: %w", match, err)
		}
		if info.IsDir() {
			continue
		}
		// Follow symlink-to-file; skip symlink-to-dir and other non-regular.
		if info.Mode()&os.ModeSymlink != 0 {
			targetInfo, err := os.Stat(match)
			if err != nil || targetInfo.IsDir() || !targetInfo.Mode().IsRegular() {
				continue
			}
		} else if !info.Mode().IsRegular() {
			continue
		}

		target := filepath.Join(d.path, filepath.Base(match))
		if err := copySourceFile(d, match, target); err != nil {
			return err
		}
	}
	return nil
}

// pruneGlob removes regular files directly under d.path whose basename is not
// among the current glob matches. Subdirectories and unmatched names that are
// not plain files are left alone (Rex prune_dir).
func pruneGlob(d *Dir) error {
	matches, err := filepath.Glob(d.sourceGlob)
	if err != nil {
		return fmt.Errorf("invalid source glob %q: %w", d.sourceGlob, err)
	}
	keep := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		keep[filepath.Base(match)] = struct{}{}
	}

	entries, err := os.ReadDir(d.path)
	if err != nil {
		return fmt.Errorf("read dest %s for prune: %w", d.path, err)
	}
	logger.Debug("pruning destination directory %s against glob %s", d.path, d.sourceGlob)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Skip anything that isn't a regular file (e.g. nested symlink dirs).
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		name := entry.Name()
		if _, ok := keep[name]; ok {
			continue
		}
		path := filepath.Join(d.path, name)
		if resource.DryRun() {
			resource.Note(fmt.Sprintf("File[%s]", path), resource.StatusWouldChange)
			logger.Info("dry-run: would prune %s", path)
			continue
		}
		logger.Debug("pruning %s", path)
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("failed to prune %s: %w", path, err)
		}
		resource.Note(fmt.Sprintf("File[%s]", path), resource.StatusChanged)
		logger.Info("pruned %s", path)
	}
	return nil
}
