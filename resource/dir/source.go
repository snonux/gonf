package dir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	opt "github.com/snonux/gonf/resource/options"
)

// copySourceTree mirrors d.source into d.path, dispatching each entry by
// kind. Symlink-ness is checked before the dir/file branches: fs.DirEntry
// reports a symlink's own type via Lstat semantics (never following it), so
// a symlink in the source tree is recreated as a symlink rather than read as
// file content.
//
// This is the apply-side twin of plan's tree packaging (plan.scanTree):
// both walk a source tree and include directories, symlinks (raw target,
// never read through), and regular files — but they deliberately differ in
// reconcile depth: copySourceTree applies attributes, notes, dry-run
// guards, and .tmpl rendering via the file/link Ensures, while scanTree
// packages raw bytes for transport and fails loudly on non-regular
// entries. Keep the entry-kind dispatch aligned between the two by hand;
// only the GLOB match rule is shared as code (GlobMatchCounts).
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
	id := fmt.Sprintf("Directory[%s]", target)

	if resource.DryRun() {
		// Dry-run must not mutate the filesystem: skip both the MkdirAll and
		// the attribute application (which would chmod/chown the destination
		// tree for real). Mirror ensureDirectorySelf's structure so a real
		// run's loud failure is previewed too: symlink and non-directory
		// targets are refused with the same dedicated messages
		// ensureDirectorySelf uses for the root, a missing target is noted as
		// would-change, and an already-existing directory is noted as ok —
		// the same status the real path notes for it, since the real path
		// only re-enforces its attributes (converged). Dry-run and real-run
		// note sets for source-tree subdirectories are identical.
		info, err := os.Lstat(target)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s is a symlink; dir resources never follow or manage a symlinked directory", target)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s exists and is not a directory", target)
			}
			resource.Note(id, resource.StatusOK)
		case os.IsNotExist(err):
			resource.Note(id, resource.StatusWouldChange)
			logger.Info("dry-run: would create directory %s", target)
		default:
			return fmt.Errorf("failed to stat %s: %w", target, err)
		}
		return nil
	}

	// Lstat before MkdirAll so the notes below can tell creating the
	// directory from re-enforcing an existing one, mirroring
	// ensureDirectorySelf's note flow for the root. No new refusals live
	// here: a non-directory is refused by MkdirAll and a planted symlink by
	// applyAttributesTo's O_NOFOLLOW|O_DIRECTORY open, exactly as before
	// this note bookkeeping existed; nothing is noted when either fails.
	_, statErr := os.Lstat(target)
	existed := statErr == nil
	switch {
	case existed:
		// Directory already exists: MkdirAll below is a no-op for it and
		// applyAttributesTo re-enforces its attributes; it is noted as ok
		// after both succeed.
	case os.IsNotExist(statErr):
		// Missing: MkdirAll creates it and it is noted as changed after
		// applyAttributesTo succeeded.
	default:
		return fmt.Errorf("failed to stat %s: %w", target, statErr)
	}

	// MkdirAll resolves intermediate path components through the kernel like
	// any other path lookup (an intermediate symlinked directory is the
	// admin's configured path); the FINAL component is what
	// applyAttributesTo's O_NOFOLLOW|O_DIRECTORY open protects, so a
	// symlink planted at target is refused (ELOOP) instead of followed.
	if err := os.MkdirAll(target, d.mode); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", target, err)
	}
	if err := applyAttributesTo(target, d.mode, d.user, d.group); err != nil {
		return err
	}
	if existed {
		// Attributes were re-enforced (converged): report ok like
		// ensureDirectorySelf's existing-directory case, which accepts the
		// same without an attribute diff for the root.
		resource.Note(id, resource.StatusOK)
		return nil
	}
	resource.Note(id, resource.StatusChanged)
	logger.Info("created directory %s", target)
	return nil
}

// copySourceSymlink recreates the symlink found at sourcePath as a symlink
// at target, preserving its raw (unresolved) link target string. This is
// correct as long as the destination tree mirrors the source tree 1:1; an
// absolute link target pointing back into the source tree itself is not
// remapped into the destination — a pre-existing conceptual limitation of
// copying a tree of symlinks.
//
// For a RELATIVE raw target the source tree is the validity authority: the
// link points at a sibling of itself, and the destination mirrors the source
// layout, so the same raw target string is correct at the destination. The
// real run therefore always delegates to link.Ensure (creation, repointing,
// .old-aside replacement and idempotency included): by the time its
// target-exists assert runs, the walk has usually materialized the
// destination-side sibling already. The dry run cannot delegate blindly —
// it does not materialize the destination (see copySourceDir), so the
// assert would refuse a valid link whose destination-side target does not
// exist YET, diverging from the real run. Instead the dry run validates the
// raw target against the SOURCE tree (sourceSymlinkTargetExists) and, when
// it resolves there, mirrors link.Ensure's note flow itself
// (noteSourceSymlinkDryRun). When the raw target is missing in the source
// tree too, the link is dangling by construction and the delegation keeps
// link.Ensure's documented refusal — identical in both modes, since nothing
// ever materializes a target that has no source counterpart.
//
// Pre-existing walk-order limitation, unchanged here: the walk applies in
// lexical order, so in a real run a link whose target is still created
// LATER in the walk (e.g. aLink -> zdir, or a relative target reaching out
// of its subtree with "..") fails link.Ensure's assert even though the
// finished tree would be valid; the dry run previews the would-be outcome
// instead of that order-dependent refusal.
func copySourceSymlink(sourcePath, target string) error {
	rawTarget, err := os.Readlink(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read symlink %s: %w", sourcePath, err)
	}

	// Absolute raw targets are out of scope: link.Ensure's assert stats the
	// raw path directly on the host, identically in both modes.
	if resource.DryRun() && !filepath.IsAbs(rawTarget) &&
		sourceSymlinkTargetExists(sourcePath, rawTarget) {
		return noteSourceSymlinkDryRun(target, rawTarget)
	}

	return link.Ensure(target, opt.WithSymlink(rawTarget))
}

// sourceSymlinkTargetExists reports whether the relative raw target of the
// symlink at sourcePath resolves to an existing entry in the SOURCE tree.
// Lstat (not Stat) is deliberate: each symlink entry of the tree is
// recreated as a symlink judged independently, so a sibling that is itself
// a symlink counts by its own entry, never by what it resolves to.
func sourceSymlinkTargetExists(sourcePath, rawTarget string) bool {
	_, err := os.Lstat(filepath.Join(filepath.Dir(sourcePath), rawTarget))
	return err == nil
}

// noteSourceSymlinkDryRun mirrors link.Ensure's note flow for a source-tree
// symlink whose relative target is already proven to exist in the source
// tree — the validity authority for relative targets, since dry-run does
// not materialize the destination and link.Ensure's destination-side
// target-exists assert would refuse the link prematurely. The mirrored
// branches (converged ok, repoint, replace, create) are exactly
// link.Ensure's, minus that assert; the real run still goes through
// link.Ensure, so repointing and .old-aside replacement keep their
// contract. One deliberate imprecision: the replace branch skips the
// stale-.old-backup refusal of the real run (link's unexported aside
// guard), previewing would-change where the real run would refuse — the
// pre-fix dry run mis-reported that scenario too (as a broken-link
// refusal), and the real run's refusal stays authoritative.
func noteSourceSymlinkDryRun(target, rawTarget string) error {
	id := fmt.Sprintf("Symlink[%s]", target)
	info, err := os.Lstat(target)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		current, err := os.Readlink(target)
		if err != nil {
			return fmt.Errorf("failed to read symlink %s: %w", target, err)
		}
		if current == rawTarget {
			logger.Debug("symlink %s already points at %s", target, rawTarget)
			resource.Note(id, resource.StatusOK)
			return nil
		}
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would repoint symlink %s", target)
	case err == nil:
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would replace %s with symlink", target)
	case os.IsNotExist(err):
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would create symlink %s -> %s", target, rawTarget)
	default:
		return fmt.Errorf("failed to stat %s: %w", target, err)
	}
	return nil
}

// copySourceFile delegates writing a single copied file to the file
// package's own primitive, using d's file-mode default (not d's directory
// mode) and passing the mechanically-derived target path verbatim — file's
// own resolve() strips a ".tmpl" suffix and computes .Param consistently, so
// dir needs no special-casing of its own.
//
// The one special case is the template Param: a .tmpl entry inside a synced
// tree renders {{.Param}} at apply time (destination env). By default file
// derives Param from the mechanical source path — on the plan path the
// ephemeral blob-extraction dir, which changes every run and would embed a
// random path into the rendered content (flapping the checksum). When plan
// apply passed the recipe's declared source dir (opt.WithSourceBase),
// override Param with declared-dir + "/" + the entry's path relative to the
// synced root — exactly what the direct (non-plan) path derives from its
// real source tree. Non-template entries are left untouched.
func copySourceFile(d *Dir, sourcePath, target string) error {
	opts := []opt.FileOption{
		opt.WithSource(sourcePath),
		opt.WithMode(d.fileMode),
		opt.WithOwner(d.user),
		opt.WithGroup(d.group),
	}
	if d.sourceBase != "" && d.source != "" && strings.HasSuffix(sourcePath, ".tmpl") {
		rel, err := filepath.Rel(d.source, sourcePath)
		if err != nil {
			return fmt.Errorf("cannot derive template param for %s: %w", sourcePath, err)
		}
		opts = append(opts, opt.WithParam(filepath.Join(d.sourceBase, rel)))
	}
	return file.Ensure(target, opts...)
}

// pruneTree removes anything under d.path that has no counterpart in
// d.source. A destination entry also counts as having a counterpart if
// d.source has the same relative path with a ".tmpl" suffix appended, since
// copySourceFile (via file.Ensure) strips that suffix when writing —
// otherwise every templated file would be pruned immediately after being
// copied. In dry-run mode nothing is removed; every would-be-pruned path is
// only noted as StatusWouldChange (the walk still descends into stale
// directories so their contents are previewed too). The real run notes each
// removed path as File[<path>] StatusChanged after its successful removal
// (mirroring pruneGlob), so tree prunes show up in the summary and gate
// daemon-reload via AnyChanged, whose Directory[<root>] matching works
// through the File[<root>/...] note convention. A fresh dry-run never
// created the destination in the first place (ensureDirectorySelf and
// copySourceDir skip their MkdirAll under dry-run), so a missing destination
// means there is nothing to prune and the walk is skipped instead of
// failing; the real path always finds the destination here because
// ensureDirectorySelf created it before this runs.
func pruneTree(d *Dir) error {
	logger.Debug("pruning destination directory %s", d.path)

	if resource.DryRun() {
		if _, err := os.Lstat(d.path); os.IsNotExist(err) {
			return nil // nothing exists to prune
		}
	}

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
		// Mirror pruneGlob's post-removal note (same id scheme, same log
		// wording) so real-run tree prunes are visible in the summary and
		// gate daemon-reload via AnyChanged. Stale directories deliberately
		// keep the File[<path>] id instead of Directory[...]: for the top
		// directory it is the same id the dry-run notes for that path (the
		// dry-run additionally previews inner entries, which RemoveAll
		// removes in one operation); AnyChanged's Directory[<root>]
		// matching works through the File[<root>/...] convention regardless.
		resource.Note(fmt.Sprintf("File[%s]", path), resource.StatusChanged)
		logger.Info("pruned %s", path)
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
// basename entries (flat, Rex ensure_dir style). A match counts exactly
// when GlobMatchCounts says so — the one glob-match rule also used by the
// prune keep-set below and by plan's glob blob packaging — so what is
// installed here can never diverge from what prune keeps or what a remote
// push packages.
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
		if !GlobMatchCounts(match, info) {
			continue
		}

		target := filepath.Join(d.path, filepath.Base(match))
		if err := copySourceFile(d, match, target); err != nil {
			return err
		}
	}
	return nil
}

// pruneGlob removes regular files directly under d.path whose basename is
// not the basename of a COUNTING glob match (dir.GlobMatchCounts) — the
// same predicate copySourceGlob installs through, so the keep-set stays in
// lockstep with the copy: nothing copied is ever pruned, and stale
// destination files whose name matches only a non-counting entry (a
// directory, dangling link, or other non-regular source entry) are
// converged away instead of being kept forever. Subdirectories and
// unmatched names that are not plain files are left alone (Rex prune_dir).
// In dry-run mode nothing is removed and would-be-pruned paths are only
// noted as StatusWouldChange; a missing destination means there is nothing
// to prune (same reasoning as pruneTree).
func pruneGlob(d *Dir) error {
	if resource.DryRun() {
		if _, err := os.Lstat(d.path); os.IsNotExist(err) {
			return nil // nothing exists to prune
		}
	}

	matches, err := filepath.Glob(d.sourceGlob)
	if err != nil {
		return fmt.Errorf("invalid source glob %q: %w", d.sourceGlob, err)
	}
	keep := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			continue // an unreadable match cannot count; its basename may be pruned
		}
		if !GlobMatchCounts(match, info) {
			continue
		}
		// The keep-set must use the WRITTEN basename: copySourceFile strips a
		// trailing ".tmpl" when the source triggers templating, so a match
		// foo.tmpl installs as foo (rendered) — pinning the raw name would
		// prune and re-copy the rendered file on every run (task 422).
		keep[KeepBasename(match)] = struct{}{}
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
