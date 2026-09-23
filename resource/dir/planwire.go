package dir

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// dirHandler, syncDirHandler, and ensureDirHandler are the plan.Handlers for
// dir's three op kinds ("dir", "sync_dir", "ensure_dir" — see (*Dir).planDraft
// and EnsurePlanDraft). They are separate types, one per Kind, because
// RegisterHandler keys a single Handler per Kind; see
// resource/pkg/planwire.go for why record-time ToOp and apply-time Apply
// live together in the resource package instead of api/packager.go's draftToOp
// and plan/apply.go's applyDir/applySyncDir/applyEnsureDir.
type dirHandler struct{}
type syncDirHandler struct{}
type ensureDirHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindDir, dirHandler{})
	plan.RegisterHandler(plan.KindSyncDir, syncDirHandler{})
	plan.RegisterHandler(plan.KindEnsureDir, ensureDirHandler{})
}

// ToOp lowers a "dir" resource draft to a plan.Op.
func (dirHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:     plan.KindDir,
		ID:     d.ID,
		Path:   d.Path,
		Absent: d.Absent,
		Prune:  d.Prune,
		Mode:   d.Mode,
		Owner:  d.Owner,
		Group:  d.Group,
		Deps:   slices.Clone(d.Deps),
	}, nil
}

// Apply creates, prunes, or removes the destination directory, mirroring
// the resource's own mode/ownership/prune/absent handling exactly.
func (dirHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	path, err := plan.ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("dir: missing path")
	}
	var opts []opt.DirOption
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.Prune {
		opts = append(opts, opt.WithPrune)
	}
	if op.Mode != "" {
		mode, err := plan.ParseMode(op.Mode)
		if err != nil {
			return fmt.Errorf("dir: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	for _, ownerOpt := range plan.OwnerGroupOptions(op) {
		opts = append(opts, ownerOpt)
	}
	return Ensure(path, opts...)
}

// ToOp lowers a "sync_dir" resource draft to a plan.Op. The Blob field is
// filled in later by api's packageDraft (api/packager.go), which packages
// the SourceDir/SourceGlob it finds in d.Payload (SyncPayload, task w62
// Layer 1) into the plan's blob store after ToOp returns. Glob records the
// WithSourceGlob flavor (schema v24): its blob is a flat match set, and
// apply must rebuild a glob sync, never a tree sync, so that WithPrune
// keeps its glob semantics (see syncDirSourceOption). A "sync_dir" draft
// without a SyncPayload is a record-time bug (planDraft always sets it),
// reported like any other handler error rather than panicking.
func (syncDirHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(SyncPayload)
	if !ok {
		return plan.Op{}, fmt.Errorf("sync_dir: draft missing dir.SyncPayload (got %T)", d.Payload)
	}
	return plan.Op{
		Op:    plan.KindSyncDir,
		ID:    d.ID,
		Path:  d.Path,
		Blob:  d.Blob,
		Mode:  d.Mode,
		Owner: d.Owner,
		Group: d.Group,
		Prune: d.Prune,
		Deps:  slices.Clone(d.Deps),
		Payload: plan.SyncDirPayload{
			SourceDir: p.SourceDir,
			Glob:      p.SourceGlob != "",
			FileMode:  p.FileMode,
		},
	}, nil
}

// Apply mirrors the synced blob tree into the destination directory,
// mirroring the resource's own WithSource/WithSourceGlob handling exactly.
// ctx.Facts is threaded down to every copied entry (ensureWithPlanFacts), so
// a .tmpl inside the synced tree renders {{.Gonf.*}} from the same plan
// facts — -profile override included — as a single-file op in this apply.
func (syncDirHandler) Apply(op plan.Op, ctx plan.ApplyContext) error {
	path, err := plan.ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("sync_dir: missing path")
	}
	src, err := syncDirBlobTree(op.Blob, ctx.PlanDir)
	if err != nil {
		return err
	}
	// A comma-ok assertion, not a "missing payload" error: Apply may see an
	// op decoded from an arbitrary plan.jsonl (mirrors resource/cron's
	// planwire.go Apply). A nil or mistyped Payload degrades to the zero
	// SyncDirPayload (Glob false, FileMode/SourceDir empty), which is
	// exactly pre-v24/pre-v6 behavior for a plan recorded before those
	// fields existed.
	p, _ := op.Payload.(plan.SyncDirPayload)
	if p.Glob {
		// Defense-in-depth (task qc2, the kc2 reviewer's follow-up): verify
		// the rebuilt pattern actually covers the resolved blob BEFORE
		// installing or pruning anything from it. See syncDirGlobGuard.
		if err := syncDirGlobGuard(path, globPattern(src), src); err != nil {
			return err
		}
	}
	opts, err := syncDirOptions(op, src)
	if err != nil {
		return err
	}
	return ensureWithPlanFacts(path, ctx.Facts, opts...)
}

// syncDirBlobTree resolves the op's blob ref under planDir and requires it
// to be a directory: a sync_dir blob is always a packaged tree (or a
// flattened glob).
func syncDirBlobTree(blob, planDir string) (string, error) {
	if blob == "" {
		return "", fmt.Errorf("sync_dir: missing blob id")
	}
	src, err := plan.Resolve(planDir, blob)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("sync_dir: blob %q: %w", blob, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sync_dir: blob %q is not a directory", blob)
	}
	return src, nil
}

// syncDirOptions translates a sync_dir op's recorded fields into the
// DirOptions the direct WithSource / WithSourceGlob path would use, sourcing
// from the resolved blob tree src. SourceDir/Glob/FileMode come from the
// op's SyncDirPayload (task 9e2); Mode/Owner/Group/Prune/Sensitive stay
// core Op fields (Prune is genuinely shared with KindDir — see Op.Prune's
// doc comment, plan/types.go). The comma-ok Payload assertion mirrors
// Apply's own (see its comment): a nil or mistyped Payload degrades to the
// zero SyncDirPayload.
func syncDirOptions(op plan.Op, src string) ([]opt.DirOption, error) {
	p, _ := op.Payload.(plan.SyncDirPayload)
	opts := []opt.DirOption{syncDirSourceOption(p, src)}
	// source_dir is the recipe's declared source directory (the glob
	// pattern's directory for the glob flavor): .tmpl files inside the
	// synced tree render {{.Param}} from it instead of the ephemeral blob
	// path, which would change every run and flap the rendered checksums.
	// Plans recorded before schema v6 carry no source_dir and keep the
	// blob-path Param.
	if p.SourceDir != "" {
		opts = append(opts, opt.WithSourceBase(p.SourceDir))
	}
	if op.Mode != "" {
		mode, err := plan.ParseMode(op.Mode)
		if err != nil {
			return nil, fmt.Errorf("sync_dir: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	if p.FileMode != "" {
		mode, err := plan.ParseMode(p.FileMode)
		if err != nil {
			return nil, fmt.Errorf("sync_dir: file_mode: %w", err)
		}
		opts = append(opts, opt.WithFileMode(mode))
	}
	for _, ownerOpt := range plan.OwnerGroupOptions(op) {
		opts = append(opts, ownerOpt)
	}
	if op.Prune {
		opts = append(opts, opt.WithPrune)
	}
	// A sensitive sync_dir op (WithSensitive at record time; the scan never
	// reads the tree) rebuilds a sensitive Dir, whose entries are written as
	// sensitive files (copySourceFile).
	if op.Sensitive {
		opts = append(opts, opt.WithSensitive)
	}
	return opts, nil
}

// syncDirSourceOption selects the sync flavor the op was recorded from. A
// glob op (schema v24 glob field) rebuilds a WithSourceGlob over every entry
// of its flat blob directory: the blob holds exactly the counting matches of
// the recipe's pattern (plan.scanGlob, regular files only), so "<blob>/*"
// selects the same basenames the direct path installs, and WithPrune then
// runs pruneGlob — removing only non-matching regular files directly under
// the destination and leaving subdirectories, symlinks and other entries
// alone. Rebuilding it as a tree sync instead would run pruneTree and delete
// every unmanaged destination entry, subdirectories included (task sb2).
// Go's "*" matches dot-names too, as on the direct path. The blob path is
// quoted so a glob metacharacter in it matches literally. Every other op —
// and a glob op recorded before v24, which carries no glob field — keeps the
// tree sync. p is the op's SyncDirPayload (task 9e2), already comma-ok
// asserted by the caller.
func syncDirSourceOption(p plan.SyncDirPayload, src string) opt.DirOption {
	if p.Glob {
		return opt.WithSourceGlob(globPattern(src))
	}
	return opt.WithSource(src)
}

// globPattern is the "<src>/*" glob pattern a glob sync_dir op rebuilds over
// its flat blob directory (see syncDirSourceOption). It is factored out so
// syncDirGlobGuard checks the EXACT pattern that install/prune will use,
// never a hand-rolled approximation that could drift from it.
func globPattern(src string) string {
	return filepath.Join(quoteGlob(src), "*")
}

// syncDirGlobGuard is a defense-in-depth check for the glob sync_dir apply
// path (task qc2, the follow-up the kc2 reviewer asked for — kc2 itself
// fixed the one known root cause, quoteGlob's rune-vs-byte bug; task yc2
// then closed a gap in the guard itself, see below). It refuses the apply
// when pattern's COUNTING matches are ZERO while blobDir — the resolved
// blob directory pattern was built from — is non-empty.
//
// A glob blob is SUPPOSED to hold only COUNTING entries by construction
// (plan.scanGlob only packages regular files, and symlinks resolved through
// to one; see plan/manifest.go), and Go's "*" matches dot-names too (unlike
// a shell glob), so a correctly built "<blobDir>/*" pattern is guaranteed to
// counting-match every entry of a non-empty blobDir — UNLESS that
// construction invariant itself has broken (a tree blob accidentally
// plumbed into a Glob op, a future WriteGlob learning to preserve symlinks,
// a dangling symlink surviving into a blob, ...), in which case blobDir can
// hold non-counting entries (e.g. a subdirectory) that filepath.Glob still
// reports as raw matches even though GlobMatchCounts — the same predicate
// pruneGlob's real keep-set is built from — rejects them. Task yc2: raw
// matches alone are therefore not a valid proxy for "the pattern still
// covers the blob"; the guard must count matches the same way pruneGlob
// does, or it can pass trivially (len(matches) != 0) while the real
// keep-set pruneGlob builds is empty. A blobDir whose counting-match count
// is zero — whether because pattern no longer names blobDir at all (kc2's
// shape) or because blobDir's entries exist but none of them count (yc2's
// shape) — can therefore never be a legitimate "nothing to sync" outcome
// when blobDir itself is non-empty; either way this guard is generic and
// catches ANY future bug with one of these shapes, not just a repeat of a
// known incident. Silently proceeding would otherwise install nothing
// (copySourceGlob) and, with WithPrune, delete every unmanaged regular file
// directly under the destination (pruneGlob's keep-set would end up empty)
// — the exact data-loss shape kc2 fixed.
//
// A genuinely empty blob directory (nothing to sync — a legitimate glob
// sync_dir whose source matched nothing at record time) is NOT refused:
// that case returns nil before filepath.Glob ever runs, leaving
// copySourceGlob/pruneGlob's normal (legitimate) empty-match handling
// untouched.
func syncDirGlobGuard(path, pattern, blobDir string) error {
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return fmt.Errorf("sync_dir: read blob dir %q: %w", blobDir, err)
	}
	if len(entries) == 0 {
		return nil // genuinely empty blob: nothing to sync, not a bug
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("sync_dir: invalid glob %q: %w", pattern, err)
	}
	if !hasCountingMatch(matches) {
		return fmt.Errorf("sync_dir: glob %q has no counting entries although blob dir %q is non-empty; refusing to install/prune %s (a correctly built pattern always counting-matches a non-empty blob, so this signals a bug in glob pattern construction or blob packaging rather than an empty source)", pattern, blobDir, path)
	}
	return nil
}

// hasCountingMatch reports whether any of matches is a COUNTING glob match
// (GlobMatchCounts) — the same predicate pruneGlob's real keep-set and
// copySourceGlob's install loop are built from, so syncDirGlobGuard's
// emptiness check shares pruneGlob's notion of "matches" instead of a raw
// filepath.Glob count that a non-counting entry (e.g. a stray subdirectory
// in the blob) could satisfy trivially. An unreadable match is not counted
// (mirroring pruneGlob's own "an unreadable match cannot count" handling)
// rather than failing the guard outright. Stops at the first counting
// match (task ed2): the guard only ever needs "is there at least one," so
// os.Lstat-ing every match in a large glob blob wasted a syscall pair per
// entry for a count nothing used past its zero-ness.
func hasCountingMatch(matches []string) bool {
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			continue
		}
		if GlobMatchCounts(match, info) {
			return true
		}
	}
	return false
}

// quoteGlob escapes the filepath.Match metacharacters in path so it matches
// only itself when used as a glob prefix (backslash escaping; gonf only
// targets Unix-like systems, where filepath.Match honors it). It walks BYTES,
// not runes: a Unix path is an arbitrary byte string that need not be valid
// UTF-8, and ranging over runes would rewrite every invalid byte as the
// 3-byte utf8.RuneError replacement character, changing the byte length and
// producing a pattern that no longer names the real blob directory —
// filepath.Glob then matches nothing, WithSourceGlob installs nothing, and
// WithPrune's keep-set is empty, so it removes every unmanaged destination
// entry instead of leaving them alone (task kc2, a data-loss regression in
// the fix task sb2 introduced this helper for).
func quoteGlob(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
		}
		b.WriteByte(path[i])
	}
	return b.String()
}

// ToOp lowers an "ensure_dir" resource draft to a plan.Op.
func (ensureDirHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:    plan.KindEnsureDir,
		ID:    d.ID,
		Path:  d.Path,
		Mode:  d.Mode,
		Owner: d.Owner,
		Group: d.Group,
		Deps:  slices.Clone(d.Deps),
	}, nil
}

// Apply ensures the destination directory exists with the recorded
// mode/ownership, mirroring EnsureDir's own option handling exactly.
func (ensureDirHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	path, err := plan.ExpandPath(op.Path)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("ensure_dir: missing path")
	}
	var opts []opt.DirOption
	if op.Mode != "" {
		mode, err := plan.ParseMode(op.Mode)
		if err != nil {
			return fmt.Errorf("ensure_dir: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	for _, ownerOpt := range plan.OwnerGroupOptions(op) {
		opts = append(opts, ownerOpt)
	}
	return Ensure(path, opts...)
}
