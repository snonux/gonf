package dir

import (
	"fmt"
	"os"
	"slices"

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
// d.SourceDir/d.SourceGlob into the plan's blob store after ToOp returns.
func (syncDirHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:        plan.KindSyncDir,
		ID:        d.ID,
		Path:      d.Path,
		Blob:      d.Blob,
		SourceDir: d.SourceDir,
		Mode:      d.Mode,
		FileMode:  d.FileMode,
		Owner:     d.Owner,
		Group:     d.Group,
		Prune:     d.Prune,
		Deps:      slices.Clone(d.Deps),
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
// DirOptions the direct WithSource path would use, sourcing from the
// resolved blob tree src.
func syncDirOptions(op plan.Op, src string) ([]opt.DirOption, error) {
	opts := []opt.DirOption{opt.WithSource(src)}
	// source_dir is the recipe's declared source directory (the glob
	// pattern's directory for the glob flavor): .tmpl files inside the
	// synced tree render {{.Param}} from it instead of the ephemeral blob
	// path, which would change every run and flap the rendered checksums.
	// Plans recorded before schema v6 carry no source_dir and keep the
	// blob-path Param.
	if op.SourceDir != "" {
		opts = append(opts, opt.WithSourceBase(op.SourceDir))
	}
	if op.Mode != "" {
		mode, err := plan.ParseMode(op.Mode)
		if err != nil {
			return nil, fmt.Errorf("sync_dir: %w", err)
		}
		opts = append(opts, opt.WithMode(mode))
	}
	if op.FileMode != "" {
		mode, err := plan.ParseMode(op.FileMode)
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
