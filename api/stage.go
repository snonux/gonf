package api

import (
	"fmt"
	"os"
	"strings"

	"github.com/snonux/gonf/plan"
)

// stageBlobs is RecordPlan's persistent-directory path: it records into a
// private staging directory and copies the blobs the finished plan references
// into planDir only after RecordPlanTo returned a plan.
//
// Why stage instead of validating first: blob refs are packaged while task
// bodies run (packageDraft, one draft at a time), long before the whole plan
// exists to be validated, and a record can also fail late for reasons that have
// nothing to do with dependencies (a cycle, a packaging error, a body error).
// Staging closes that whole class at once: whatever makes the record fail, the
// destination was never touched. The staging directory is temporary storage
// removed on return, so a failed record leaves nothing behind either.
func stageBlobs(planID, planDir string, taskNames []string) ([]plan.Op, error) {
	stage, err := os.MkdirTemp("", "gonf-plan-stage-*")
	if err != nil {
		return nil, fmt.Errorf("RecordPlan: staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	ops, err := RecordPlanTo(planID, plan.NewStore(stage), taskNames...)
	if err != nil {
		return nil, err
	}
	if err := commitStagedBlobs(ops, stage, planDir); err != nil {
		return nil, fmt.Errorf("RecordPlan: plan dir: %w", err)
	}
	return ops, nil
}

// commitStagedBlobs makes planDir exist (owner-only) and copies every blob the
// recorded ops reference from the staging directory into it. Blobs of earlier
// plans that this plan does not reference stay untouched; a blob with the same
// ref is replaced (the store's own write semantics: files atomically, trees
// cleared and recreated). It is the only step that writes planDir, and it runs
// after every validation, so what can still fail here is I/O on the destination
// itself (permissions, full disk); those errors may leave some blobs copied,
// which is unavoidable without transactional directories.
func commitStagedBlobs(ops []plan.Op, stage, planDir string) error {
	if err := plan.SecureDir(planDir); err != nil {
		return err
	}
	dest := plan.NewStore(planDir)
	copied := make(map[string]bool)
	for _, op := range ops {
		if op.Blob == "" || copied[op.Blob] {
			continue
		}
		copied[op.Blob] = true
		if err := copyStagedBlob(stage, dest, op.Blob); err != nil {
			return err
		}
	}
	return nil
}

// copyStagedBlob copies one staged blob (a single file, or a tree/glob
// directory) into dest under the same ref. Trees go back through WriteTree,
// which packages through the same neutral manifest that produced the staged
// copy, so the result is identical to what a direct write would have made.
func copyStagedBlob(stage string, dest *plan.Store, ref string) error {
	src, err := plan.Resolve(stage, ref)
	if err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("staged blob %q: %w", ref, err)
	}
	name := strings.TrimPrefix(ref, "blobs/")
	var got string
	if info.IsDir() {
		got, err = dest.WriteTree(name, src)
	} else {
		var data []byte
		if data, err = os.ReadFile(src); err != nil {
			return fmt.Errorf("staged blob %q: %w", ref, err)
		}
		got, err = dest.WriteFile(name, data)
	}
	if err != nil {
		return err
	}
	if got != ref {
		return fmt.Errorf("staged blob %q was written as %q", ref, got)
	}
	return nil
}
