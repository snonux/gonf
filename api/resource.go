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
func Apply() error {
	if plan.Recording() || resource.PlanDraftRecording() {
		return fmt.Errorf("Apply: cannot apply while plan recording is active")
	}

	drafts := resource.RegisteredPlanDrafts()
	registered := resource.RegisteredIDs()
	if len(registered) == 0 {
		return nil
	}
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

	planDir, err := os.MkdirTemp("", "gonf-apply-*")
	if err != nil {
		return fmt.Errorf("Apply: temp plan dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(planDir) }()

	recSession.recordedBlobRefs = make(map[string]string)
	recSession.recordingElevate = false
	store := plan.NewStore(planDir)
	ops := []plan.Op{{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}}
	for _, draft := range drafts {
		op, err := packageDraft(draft, store)
		if err != nil {
			return err
		}
		ops = append(ops, op)
	}
	return ApplyPlan(ops, planDir)
}
