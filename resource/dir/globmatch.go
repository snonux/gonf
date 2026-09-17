package dir

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/plan"
)

// GlobMatchCounts is dir's name for the single definition of the
// WithSourceGlob match rule, consumed by dir's copy path (copySourceGlob)
// and prune keep-set (pruneGlob). The canonical implementation lives in
// plan.GlobMatchCounts (also used by plan's own glob blob packaging,
// plan.scanGlob) — dir delegates rather than redefining it, since dir may
// import plan but plan must never import dir back (see docs/plan.md on the
// plan/resource package split). A glob match counts when it is a regular
// file or a symlink that resolves to a regular file (read through into
// content); directories, dangling links, and other non-regular entries are
// skipped.
//
// info must be the os.Lstat result for match (the entry's own type, never
// following the link); resolving a symlink happens here via os.Stat. Every
// consumer of a source glob must classify its matches through this
// predicate so install, prune, and blob packaging cannot diverge — a
// divergence would make destination files flap between install and prune
// on each run.
func GlobMatchCounts(match string, info os.FileInfo) bool {
	return plan.GlobMatchCounts(match, info)
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
