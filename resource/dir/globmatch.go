package dir

import (
	"os"
	"path/filepath"
	"strings"
)

// GlobMatchCounts is the single definition of the WithSourceGlob match
// rule, consumed by dir's copy path (copySourceGlob), dir's prune keep-set
// (pruneGlob), and plan's glob blob packaging (plan.scanGlob): a glob
// match counts when it is a regular file or a symlink that resolves to a
// regular file (read through into content); directories, dangling links,
// and other non-regular entries are skipped.
//
// info must be the os.Lstat result for match (the entry's own type, never
// following the link); resolving a symlink happens here via os.Stat. Every
// consumer of a source glob must classify its matches through this
// predicate so install, prune, and blob packaging cannot diverge — a
// divergence would make destination files flap between install and prune
// on each run.
func GlobMatchCounts(match string, info os.FileInfo) bool {
	switch {
	case info.IsDir():
		return false
	case info.Mode()&os.ModeSymlink != 0:
		// Follow a symlink to a regular file; skip symlink-to-dir, dangling
		// links, and every other non-regular target.
		target, err := os.Stat(match)
		return err == nil && target.Mode().IsRegular()
	case info.Mode().IsRegular():
		return true
	default:
		return false
	}
}

// KeepBasename returns the basename the copy path writes a counting glob
// match under: copySourceFile delegates to file.Ensure, whose targetPath
// strips a trailing ".tmpl" from the caller-given path when the source
// triggers templating, so a match foo.tmpl installs as foo (rendered) while
// every other match keeps its own basename. The prune keep-set must use the
// same name mapping, or a templated match is pruned and re-copied on every
// run (task 422).
func KeepBasename(match string) string {
	base := filepath.Base(match)
	return strings.TrimSuffix(base, ".tmpl")
}
