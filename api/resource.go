package api

import (
	"fmt"
	"sort"
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
// and the same privilege split used by Run: the lowered ops are put into
// dependency order (orderForPrivilegeSplit) and cut into privilege chunks
// (plan.SplitPrivilegeChunks), and an op marked elevate (a
// command with WithElevate) runs in its own chunk through the process-wide
// privilege mode (SetPrivilege, the CLI -privilege flag) exactly as
// ApplyChunks runs it for Run and `gonf apply`: a sudo/doas re-exec of this
// binary's `apply` subcommand, or in-process when the mode is none and the
// process already runs as root. Before this split, an elevated op silently
// ran in-process as the calling user. Source data that cannot fit inline is
// staged in a temporary plan directory and removed after apply.
//
// A plan without any elevated op forms one unprivileged chunk and takes
// exactly the pre-split path (one ApplyPlan call, unprefixed errors), so
// ordinary recipes behave as before. The elevated re-exec needs a binary
// whose command line understands `apply` (a program built on gonf's CLI),
// as for Run.
//
// Apply holds the WHOLE registered plan, so it is a controller-side entry
// point in the same sense as ApplyChunks and remote.Delivery.ToHost: it runs
// the dependency and change-gate pre-flight itself (validateApplyDeps) over
// the privilege chunks before anything is applied. plan.Apply and ApplyPlan do
// not, because they also execute single privilege chunks whose deps
// legitimately live in an earlier chunk; without this check a typo'd
// DependsOn ID would be silently treated as already satisfied. Apply never
// records through RecordPlanTo (it lowers the registered drafts directly), so
// the record-time pre-flight does not cover it either.
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

	// Removed on return and, through logger.OnFatal, on a fail-fast
	// logger.Fatal while resources are packaged or applied (tempPlanDir).
	planDir, removePlanDir, err := tempPlanDir("gonf-apply-*")
	if err != nil {
		return fmt.Errorf("Apply: temp plan dir: %w", err)
	}
	defer removePlanDir()

	ops, err := packageApplyOps(drafts, plan.NewStore(planDir))
	if err != nil {
		return err
	}
	return applyPackagedOps(ops, planDir)
}

// requireDraftsForAll refuses the apply when the registered resources and
// their plan drafts do not line up one to one:
//
//   - a registered resource without a draft (e.g. registered through the
//     low-level resource.Register without a draft) cannot be expressed as a
//     plan op, so applying the rest would silently skip it;
//   - a draft whose ID is not registered would be applied although no
//     resource owns it. The repository never produces such a draft (it only
//     keeps drafts of registered IDs), so this guards the invariant the
//     snapshot relies on rather than a reachable user error.
//
// Both lists are named in the error, so the message is never empty. Duplicate
// registered IDs are harmless (each still has its draft) and are not an error.
func requireDraftsForAll(drafts []resource.PlanDraft, registered []string) error {
	draftIDs := make(map[string]struct{}, len(drafts))
	for _, draft := range drafts {
		draftIDs[draft.ID] = struct{}{}
	}
	registeredIDs := make(map[string]struct{}, len(registered))
	var missing []string
	for _, id := range registered {
		registeredIDs[id] = struct{}{}
		if _, ok := draftIDs[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Apply: registered resources without plan drafts: %s", strings.Join(missing, ", "))
	}
	var orphans []string
	for id := range draftIDs {
		if _, ok := registeredIDs[id]; !ok {
			orphans = append(orphans, id)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return fmt.Errorf("Apply: plan drafts for unregistered resources: %s", strings.Join(orphans, ", "))
	}
	return nil
}

// packageApplyOps converts the registered drafts into the plan ops Apply
// executes, behind a fresh plan header. Blobs too large for inline content are
// staged in store, whose directory the caller owns.
//
// Side effect: it resets three process-wide recording-session fields,
// recSession.recordedBlobRefs, recSession.recordingElevate and
// recSession.recordingStack. packageDraft (shared with the record path)
// consults all three — the first to detect blob-ref collisions between
// different resources, the second to fold a Privileged() task's elevate flag
// into every op, the third to name the task in a draft error (draftError) —
// and Apply is not a recording session, so without this reset a blob ref,
// elevate flag or task name left behind by an earlier RecordPlan in the same
// process would leak into (falsely collide with, elevate, or mislabel) the
// Apply's ops.
func packageApplyOps(drafts []resource.PlanDraft, store plan.BlobStore) ([]plan.Op, error) {
	recSession.recordedBlobRefs = make(map[string]string)
	recSession.recordingElevate = false
	recSession.recordingStack = nil
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

// applyPackagedOps runs the pre-flight over the privilege chunks of ops and
// applies them.
//
// Without an elevated op the whole plan is one unprivileged chunk whose ops
// are ops itself, so it is validated as that chunk and goes straight to
// ApplyPlan: the pre-split behaviour and error wording of Apply, byte for
// byte. With an elevated op the ops are first put into dependency order
// grouped by privilege class (orderForPrivilegeSplit; Apply's draft order is
// only a sort by resource ID), then split, validated and applied by
// ApplyChunks under processPrivilege. ApplyChunks repeats the
// plan.ValidateChunks pre-flight on purpose (it also serves already-recorded
// plans); it cannot fail here, since both validate the same split.
func applyPackagedOps(ops []plan.Op, planDir string) error {
	elevated := anyElevated(ops)
	if elevated {
		ops = orderForPrivilegeSplit(ops)
	}
	if err := validateApplyDeps(plan.SplitPrivilegeChunks(ops)); err != nil {
		return err
	}
	if !elevated {
		return ApplyPlan(ops, planDir)
	}
	return ApplyChunks(ops, planDir, processPrivilege)
}

// anyElevated reports whether an op of the plan must run elevated.
func anyElevated(ops []plan.Op) bool {
	for _, op := range ops {
		if op.Elevate {
			return true
		}
	}
	return false
}

// validateApplyDeps is the dependency and change-gate pre-flight for Apply,
// run over the privilege chunks Apply is about to apply (the same split
// ApplyChunks uses). For a plan without elevated ops that is exactly one
// chunk, so plan.ValidateChunks reduces to "every dependency and every watch
// is recorded somewhere in the plan": a dep or watch naming no registered
// resource (a typo, or a resource that was never registered) is refused
// before any resource is applied, matching what the legacy repository path
// reported as "depended upon but not registered". A dep recorded LATER in the
// same chunk is fine — plan.Apply's dependency sort reorders it. With
// elevated ops, chunks apply in order as separate plan.Apply runs, so the
// same rules as for Run and push hold: a watch across chunks is refused (the
// gate could never see the other process's change report), and so is a dep
// on a LATER chunk, which orderForPrivilegeSplit rules out except for a
// dependency cycle, left in place for the pre-flight or engine to report.
//
// The refusal is re-worded in registered-resource terms with a single "Apply:"
// prefix (preflightChunks), while errors.As still finds the typed
// *plan.DanglingDepError / *plan.DanglingWatchError. Running the change-gate
// half here, before anything is applied, also means a dangling WatchChanges
// gets the same wording as a dangling OnChange instead of the plan engine's raw
// "plan: op ... watches ..." message from plan.Apply's own gate check.
func validateApplyDeps(chunks []plan.Chunk) error {
	return preflightChunks("Apply", fixHintApply, chunks)
}
