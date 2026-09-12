package api

import (
	"fmt"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// RecordPlan runs the named activated tasks in plan-record mode: resource
// registration emits plan.Op lines instead of applying. Content/blob packaging
// may be stubbed (empty content_b64 or a path ref in blob).
func RecordPlan(planID string, taskNames ...string) ([]plan.Op, error) {
	ensureActivated()

	if planID == "" {
		return nil, fmt.Errorf("RecordPlan: plan id must not be empty")
	}
	if len(taskNames) == 0 {
		return nil, fmt.Errorf("RecordPlan: no tasks specified")
	}

	plan.ResetRecord()
	plan.SetRecording(true)
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) {
		plan.Record(draftToOp(d))
	})
	defer func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
	}()

	for _, name := range taskNames {
		tasksMu.Lock()
		t, ok := tasks[name]
		tasksMu.Unlock()
		if !ok {
			return nil, fmt.Errorf("unknown task %q", name)
		}

		resource.ResetRepository()
		t.fn()
		// Intentionally skip resource.Apply — plan-record mode only.
	}

	ops := plan.FinishRecord(planID)
	plan.ResetRecord()
	return ops, nil
}

func draftToOp(d resource.PlanDraft) plan.Op {
	op := plan.Op{
		ID:         d.ID,
		Path:       d.Path,
		Symlink:    d.Symlink,
		Hardlink:   d.Hardlink,
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
