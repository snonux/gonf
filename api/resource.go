package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
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
// and the same privilege split used by Run: when an op is marked elevate,
// the lowered ops are put into dependency order (orderForPrivilegeSplit) and
// cut into privilege chunks (plan.SplitPrivilegeChunks), and the elevated op (a
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
// ordinary recipes behave as before. A plan with one is refused before
// anything is applied when it has a dependency cycle, when the privilege
// mode is none and the process is not root, or when the process does not
// run gonf's CLI (the re-exec runs `<this binary> apply <chunk>`, which in
// any other program would re-run its own main as root; Run refuses the same
// way, see preflightElevation).
//
// A declaration error reported earlier (internal/declerr: DSL misuse such as
// an unsupported option or a duplicate resource, a failed MustSecret) refuses
// the apply before anything runs, since the registered set is incomplete.
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
	if err := declerr.First(); err != nil {
		return err
	}

	drafts := resource.RegisteredPlanDrafts()
	registered := resource.RegisteredIDs()
	if len(registered) == 0 {
		if anyRecordFailed {
			// Some earlier RecordPlanTo/Run call in this process failed
			// its record — for any of the reasons RecordPlanTo can fail,
			// not only declared misuse (task tc2) — and RecordPlanTo
			// reset the repository on that failure, so there is nothing
			// left to register-check against. Checked only when the
			// repository is empty: once anything new is registered
			// (through a later record or directly), it is unrelated to
			// the stale failure and applying it is exactly what the
			// caller now asked for, so it must not be refused on the old
			// failure's account (that also keeps this flag from having to
			// be cleared by every test and caller that intentionally
			// fails a record and later registers or applies something
			// else in the same process).
			return fmt.Errorf("Apply: refusing: an earlier record failed")
		}
		return nil
	}
	if err := requireDraftsForAll(drafts, registered); err != nil {
		return err
	}

	// Removed on every return path (tempPlanDir).
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
// Apply is not a recording session, so it packages through its own fresh
// draftPackager (newDraftPackager): blob-ref collisions are checked among
// these drafts only, no Privileged() elevate flag is folded in, and a draft
// error names no task. Nothing an earlier RecordPlan in the same process left
// in the recording session is read, and the session is not touched.
func packageApplyOps(drafts []resource.PlanDraft, store plan.BlobStore) ([]plan.Op, error) {
	packager := newDraftPackager(store)
	ops := []plan.Op{{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}}
	for _, draft := range drafts {
		op, err := packager.packageDraft(draft)
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
// are ops itself: it is validated as that chunk and goes straight to
// ApplyPlan, unsorted — the pre-split behaviour and error wording of Apply,
// byte for byte (a dependency cycle is still reported by the engine, before
// it applies anything). With an elevated op, applyElevatedOps takes over.
func applyPackagedOps(ops []plan.Op, planDir string) error {
	if anyElevated(ops) {
		return applyElevatedOps(ops, planDir)
	}
	if err := validateApplyDeps(plan.SplitPrivilegeChunks(ops), nil); err != nil {
		return err
	}
	return ApplyPlan(ops, planDir)
}

// applyElevatedOps applies a plan with elevated ops as privilege chunks.
// Everything that can refuse it runs before the first chunk applies, because
// chunks are separate plan.Apply runs (the elevated one a separate root
// process) and a refusal from a later chunk would come after earlier chunks
// had mutated the host:
//
//   - orderForPrivilegeSplit puts the ops into dependency order with as few
//     chunks as possible, keeping a change-gated resource in the chunk of the
//     same-class resources it watches (Apply's draft order is only a sort by
//     resource ID), and refuses a dependency cycle (a self-dependency
//     included), naming it;
//   - validateApplyDeps refuses dangling deps and watches that still cross
//     the privilege boundary;
//   - preflightElevation refuses elevated chunks that could not run: mode
//     none in a non-root process, or a process not running gonf's CLI, whose
//     sudo/doas re-exec would run its own main again as root.
//
// The chunks are then applied by applySplitChunks, the loop ApplyChunks uses,
// without repeating those checks. A chunk failure is reported as "Apply:
// <class> resources <IDs>: ..." (resourceChunkLabel) rather than with
// ApplyChunks' chunk index: Apply's caller never saw a chunk order, only the
// resources it registered.
func applyElevatedOps(ops []plan.Op, planDir string) error {
	ops, conflicts, err := orderForPrivilegeSplit(ops)
	if err != nil {
		return err
	}
	chunks := plan.SplitPrivilegeChunks(ops)
	if err := validateApplyDeps(chunks, conflicts); err != nil {
		return err
	}
	if err := preflightElevation(chunks, processPrivilege); err != nil {
		return fmt.Errorf("Apply: %w", err)
	}
	if err := applySplitChunks(context.Background(), chunks, planDir, processPrivilege, resourceChunkLabel); err != nil {
		return fmt.Errorf("Apply: %w", err)
	}
	return nil
}

// resourceChunkLabel names an Apply chunk by its privilege class and
// resources: "elevated resources Command[a], File[b]", listing at most three
// IDs and counting the rest.
func resourceChunkLabel(_ int, ch plan.Chunk) string {
	ids := chunkResourceIDs(ch)
	if len(ids) > 3 {
		ids = append(ids[:3:3], fmt.Sprintf("and %d more", len(ids)-3))
	}
	return privilegeClass(ch.Elevate) + " resources " + strings.Join(ids, ", ")
}

// privilegeClass is the user-facing name of a privilege class.
func privilegeClass(elevate bool) string {
	if elevate {
		return "elevated"
	}
	return "unprivileged"
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
// before any resource is applied. A dep recorded LATER in the
// same chunk is fine — plan.Apply's dependency sort reorders it. With
// elevated ops, chunks apply in order as separate plan.Apply runs, so the
// same rules as for Run and push hold: a watch across chunks is refused (the
// gate could never see the other process's change report), and so is a dep
// on a LATER chunk, which orderForPrivilegeSplit's sort rules out for Apply
// (it refuses a dependency cycle itself, before this check).
//
// The refusal is re-worded in registered-resource terms with a single "Apply:"
// prefix (preflightChunks), while errors.As still finds the typed
// *plan.DanglingDepError / *plan.DanglingWatchError. A watch across chunks is
// worded in privilege classes, not chunk indexes (crossChunkWatchRefusal),
// since Apply chose the chunks itself; conflicts (from orderForPrivilegeSplit,
// nil without elevated ops) names the kept watches a refused one conflicts
// with. Running the change-gate half here,
// before anything is applied, also means a dangling WatchChanges gets the
// same wording as a dangling OnChange instead of the plan engine's raw "plan:
// op ... watches ..." message from plan.Apply's own gate check.
func validateApplyDeps(chunks []plan.Chunk, conflicts watchConflicts) error {
	if err := crossChunkWatchRefusal("Apply", chunks, conflicts); err != nil {
		return err
	}
	return preflightChunks("Apply", fixHintApply, chunks)
}
