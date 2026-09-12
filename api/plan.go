package api

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// RecordPlan runs the named tasks in plan-record mode: resource registration
// emits plan.Op lines instead of applying. Tasks are looked up as candidates
// (not Activate-filtered) so When* recipes become when_begin/when_end rather
// than being resolved on the controller. InstallFile sources are packaged as
// content_b64 (or blob sidecars when large); SyncDir trees are copied under
// planDir/blobs/. planDir may be empty when no SyncDir or large-file packaging
// is needed.
//
// Nested Run calls while recording append into the same plan (used by Aggregate).
func RecordPlan(planID, planDir string, taskNames ...string) ([]plan.Op, error) {
	var store plan.BlobStore
	if planDir != "" {
		if err := os.MkdirAll(planDir, 0o750); err != nil {
			return nil, fmt.Errorf("RecordPlan: plan dir: %w", err)
		}
		store = plan.NewStore(planDir)
	}
	return RecordPlanTo(planID, store, taskNames...)
}

// RecordPlanTo is like RecordPlan but packages blobs into store (disk or memory).
// Pass a nil store only when tasks need no blob packaging.
func RecordPlanTo(planID string, store plan.BlobStore, taskNames ...string) ([]plan.Op, error) {
	if planID == "" {
		return nil, fmt.Errorf("RecordPlan: plan id must not be empty")
	}
	if len(taskNames) == 0 {
		return nil, fmt.Errorf("RecordPlan: no tasks specified")
	}

	plan.ResetRecord()
	plan.SetRecording(true)
	var packErr error
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) {
		if packErr != nil {
			return
		}
		op, err := packageDraft(d, store)
		if err != nil {
			packErr = err
			return
		}
		plan.Record(op)
	})
	defer func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
	}()

	if err := recordTaskBodies(taskNames, &packErr); err != nil {
		return nil, err
	}
	if packErr != nil {
		return nil, packErr
	}

	ops := plan.FinishRecord(planID)
	plan.ResetRecord()
	return ops, nil
}

// recordTaskBodies appends ops for taskNames into the current plan session.
// packErr is shared with the draft recorder callback.
func recordTaskBodies(taskNames []string, packErr *error) error {
	for _, name := range taskNames {
		c, ok := findCandidate(name)
		if !ok {
			return fmt.Errorf("unknown task %q", name)
		}

		wrapWhen, err := planWhenForCandidate(c)
		if err != nil {
			return err
		}
		if len(wrapWhen) > 0 {
			plan.Record(plan.Op{
				Op:  plan.KindWhenBegin,
				ID:  "when." + name,
				All: wrapWhen,
			})
		}

		resource.ResetRepository()
		c.fn()
		if packErr != nil && *packErr != nil {
			return *packErr
		}

		if len(wrapWhen) > 0 {
			plan.Record(plan.Op{Op: plan.KindWhenEnd})
		}
	}
	return nil
}

// ApplyPlan applies ops using DetectFacts(). planDir is the blob sidecar root.
func ApplyPlan(ops []plan.Op, planDir string) error {
	f := DetectFacts()
	return plan.Apply(ops, plan.Facts{
		GOOS:     f.GOOS,
		Profile:  f.Profile,
		Hostname: f.Hostname,
	}, planDir)
}

// planWhenForCandidate returns serializable when predicates, or nil when the
// task has no When. Opaque When predicates must still pass on the controller.
func planWhenForCandidate(c taskCandidate) ([]plan.Predicate, error) {
	if len(c.when) == 0 {
		return nil, nil
	}
	if !c.opaqueWhen && len(c.planWhen) > 0 {
		out := make([]plan.Predicate, len(c.planWhen))
		copy(out, c.planWhen)
		return out, nil
	}
	if !whenPasses(c.when, DetectFacts()) {
		return nil, fmt.Errorf("RecordPlan: task %q When predicates fail on controller and are not serializable", c.name)
	}
	return nil, nil
}

func packageDraft(d resource.PlanDraft, store plan.BlobStore) (plan.Op, error) {
	op := draftToOp(d)
	switch {
	case d.SourcePath != "":
		data, err := os.ReadFile(d.SourcePath)
		if err != nil {
			return op, fmt.Errorf("package file %s: %w", d.SourcePath, err)
		}
		if len(data) > plan.MaxInlineContent {
			if store == nil {
				return op, fmt.Errorf("package file %s: exceeds inline limit and no plan dir for blobs", d.SourcePath)
			}
			ref, err := store.WriteFile(blobName(d), data)
			if err != nil {
				return op, err
			}
			op.Blob = ref
			op.ContentB64 = ""
		} else {
			op.ContentB64 = base64.StdEncoding.EncodeToString(data)
			op.Blob = ""
		}
	case d.SourceGlob != "":
		if store == nil {
			return op, fmt.Errorf("package sync_dir %s: plan dir required for blob packaging", d.SourceGlob)
		}
		ref, err := store.WriteGlob(blobName(d), d.SourceGlob)
		if err != nil {
			return op, err
		}
		op.Blob = ref
	case d.SourceDir != "":
		if store == nil {
			return op, fmt.Errorf("package sync_dir %s: plan dir required for blob packaging", d.SourceDir)
		}
		ref, err := store.WriteTree(blobName(d), d.SourceDir)
		if err != nil {
			return op, err
		}
		op.Blob = ref
	}
	return op, nil
}

func blobName(d resource.PlanDraft) string {
	if base := filepath.Base(d.Path); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	if d.ID != "" {
		return d.ID
	}
	if d.SourcePath != "" {
		return filepath.Base(d.SourcePath)
	}
	if d.SourceDir != "" {
		return filepath.Base(d.SourceDir)
	}
	if d.SourceGlob != "" {
		dir := filepath.Dir(d.SourceGlob)
		if base := filepath.Base(dir); base != "" && base != "." {
			return base
		}
		return "glob"
	}
	return "blob"
}

func draftToOp(d resource.PlanDraft) plan.Op {
	op := plan.Op{
		ID:         d.ID,
		Path:       d.Path,
		Symlink:    d.Symlink,
		Hardlink:   d.Hardlink,
		Target:     d.Target,
		Mode:       d.Mode,
		FileMode:   d.FileMode,
		ContentB64: d.ContentB64,
		Blob:       d.Blob,
		Prune:      d.Prune,
		Absent:     d.Absent,
		AddLine:    d.AddLine,
		RemoveLine: d.RemoveLine,
		Name:       d.Name,
		Bin:        d.Bin,
		Args:       d.Args,
		Dir:        d.Dir,
		Env:        d.Env,
		Creates:    d.Creates,
		Unless:     draftGuard(d.Unless),
		OnlyIf:     draftGuard(d.OnlyIf),
		User:       d.User,
		EnableOnly: d.EnableOnly,
		IfChanged:  d.IfChanged,
		Watch:      d.Watch,
	}
	switch d.Kind {
	case "file":
		op.Op = plan.KindFile
	case "dir":
		op.Op = plan.KindDir
	case "sync_dir":
		op.Op = plan.KindSyncDir
	case "link":
		op.Op = plan.KindLink
	case "package":
		op.Op = plan.KindPackage
	case "command":
		op.Op = plan.KindCommand
	case "ensure_dir":
		op.Op = plan.KindEnsureDir
	case "link_if_exists":
		op.Op = plan.KindLinkIfExists
	case "timer":
		op.Op = plan.KindTimer
	case "daemon_reload":
		op.Op = plan.KindDaemonReload
	default:
		op.Op = plan.Kind(d.Kind)
	}
	return op
}

func draftGuard(g *resource.PlanGuardDraft) *plan.Guard {
	if g == nil {
		return nil
	}
	return &plan.Guard{
		Bin:          g.Bin,
		Args:         g.Args,
		ExpectStdout: g.ExpectStdout,
		ExpectExit:   g.ExpectExit,
	}
}
