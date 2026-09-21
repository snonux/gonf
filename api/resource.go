package api

import (
	"fmt"
	"os"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// Resource represents a system resource managed by gonf.
type Resource interface {
	ID() string
	String() string
	// Dependencies returns the flattened resource IDs this value represents,
	// so it can be passed to the DependsOn option. A single resource yields
	// its own ID; a Multi yields the IDs of all its members.
	Dependencies() []string
}

// Apply applies every resource registered so far through the same plan engine
// used by Run, push, and fleet. Source data that cannot fit inline is staged
// in a temporary plan directory and removed after apply.
//
// Apply hands the WHOLE registered plan to the engine as one unit, so it is a
// controller-side entry point in the same sense as ApplyChunks and
// remote.PushChunks: it runs the dangling-dependency pre-flight itself
// (validateApplyDeps) before anything is applied. plan.Apply and ApplyPlan do
// not, because they also execute single privilege chunks whose deps
// legitimately live in an earlier chunk; without this check a typo'd
// DependsOn ID would be silently treated as already satisfied.
func Apply() error {
	if plan.Recording() || resource.PlanDraftRecording() {
		return fmt.Errorf("Apply: cannot apply while plan recording is active")
	}

	drafts := resource.RegisteredPlanDrafts()
	registered := resource.RegisteredIDs()
	if len(registered) == 0 {
		return nil
	}
	if err := requireDraftsForAll(drafts, registered); err != nil {
		return err
	}

	planDir, err := os.MkdirTemp("", "gonf-apply-*")
	if err != nil {
		return fmt.Errorf("Apply: temp plan dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(planDir) }()

	ops, err := packageApplyOps(drafts, plan.NewStore(planDir))
	if err != nil {
		return err
	}
	if err := validateApplyDeps(ops); err != nil {
		return err
	}
	return ApplyPlan(ops, planDir)
}

// requireDraftsForAll refuses the apply when any registered resource has no
// plan draft: such a resource (e.g. registered through the low-level
// resource.Register without a draft) cannot be expressed as a plan op, so
// applying the rest would silently skip it.
func requireDraftsForAll(drafts []resource.PlanDraft, registered []string) error {
	draftIDs := make(map[string]struct{}, len(drafts))
	for _, draft := range drafts {
		draftIDs[draft.ID] = struct{}{}
	}
	var missing []string
	for _, id := range registered {
		if _, ok := draftIDs[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 || len(draftIDs) != len(registered) {
		return fmt.Errorf("Apply: registered resources without plan drafts: %s", strings.Join(missing, ", "))
	}
	return nil
}

// packageApplyOps converts the registered drafts into the plan ops Apply
// executes, behind a fresh plan header. Blobs too large for inline content are
// staged in store, whose directory the caller owns.
func packageApplyOps(drafts []resource.PlanDraft, store plan.BlobStore) ([]plan.Op, error) {
	recSession.recordedBlobRefs = make(map[string]string)
	recSession.recordingElevate = false
	ops := []plan.Op{{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}}
	for _, draft := range drafts {
		op, err := packageDraft(draft, store)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// validateApplyDeps is the dangling-dependency pre-flight for Apply. The
// whole registered plan is applied by a single plan.Apply, i.e. it forms
// exactly one privilege chunk, so plan.ValidateChunkDeps over that one chunk
// reduces to "every dep is recorded somewhere in the plan": a dep naming no
// registered resource (a typo, or a resource that was never registered) is
// refused before any resource is applied, matching what the legacy repository
// path reported as "depended upon but not registered". A dep recorded LATER
// in the plan is fine here — plan.Apply's dependency sort reorders it — so
// only the dangling case can fail.
func validateApplyDeps(ops []plan.Op) error {
	if err := plan.ValidateChunkDeps([][]plan.Op{ops}); err != nil {
		return fmt.Errorf("Apply: %w", err)
	}
	return nil
}
