package resource

import (
	"fmt"
	"sort"
)

// draftAmender is the record-mode sink for amended drafts (see
// SetPlanDraftAmender). It is guarded by draftMu like draftRecorder.
var draftAmender func(PlanDraft) error

// SetPlanDraftAmender installs the sink AmendRegistered forwards an amended
// draft to. RecordPlanTo (api/plan.go) installs it next to the draft
// recorder for one recording session: it lowers the draft exactly like a
// recorded one (packageDraft/draftToOp, including the task's privilege) and
// replaces the op recorded earlier (plan.AmendRecorded), refusing when that
// is unsound. Pass nil to disable it; without a sink (api.Apply, the legacy
// path, unit tests) only the repository is amended.
func SetPlanDraftAmender(fn func(PlanDraft) error) {
	draftMu.Lock()
	defer draftMu.Unlock()
	draftAmender = fn
}

// PlanDraftAmending reports whether an amend sink is installed, mirroring
// PlanDraftRecording; tests use it to check that RecordPlanTo clears the
// sink on every return path.
func PlanDraftAmending() bool {
	draftMu.Lock()
	defer draftMu.Unlock()
	return draftAmender != nil
}

// RegisteredWatchTargets returns the ids registered in the current recipe
// scope whose change fires a gate watching any of watch (watchCovers, the
// AnyChanged rule): each watched id itself, and for a watched Directory[p]
// also every File[p] / File[p/…] registered here. The order is watch order,
// with a directory's files sorted after it; duplicates are dropped. A
// merged daemon-reload uses it to order itself after everything that can
// fire it, because it keeps the first declaration's recorded position.
func RegisteredWatchTargets(watch ...string) []string {
	r := getRepository()
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.registered))
	for id := range r.registered {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, w := range watch {
		if _, ok := r.registered[w]; ok {
			add(w)
		}
		if _, isDir := directoryNotePath(w); !isDir {
			continue
		}
		for _, id := range ids {
			if id != w && watchCovers(w, id) {
				add(id)
			}
		}
	}
	return out
}

// Registered returns the resource registered under id in the current recipe
// scope (the repository since the last ResetRepository: one task body or one
// when-fragment) together with its applier, which is the concrete resource
// value its Present function passed to Register. ok is false when nothing
// with that ID is registered in this scope.
//
// It lets a kind that is a singleton per scope (daemon-reload: one per
// systemd bus) find its earlier declaration and fold a later one into it
// (see AmendRegistered) instead of tripping Register's duplicate-ID refusal.
func Registered(id string) (res Resource, applier Applier, ok bool) {
	r := getRepository()
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok = r.registered[id]
	if !ok {
		return Resource{}, nil, false
	}
	return res, res.applier, true
}

// AmendRegistered updates the resource registered under draft.ID in the
// current recipe scope with a new plan draft and extra dependency edges, for
// an owner that folds a later declaration into it. All checks run before
// anything changes, so on error the repository and the recorded plan are
// exactly as before (the owner must then not commit its own state either):
//
//   - draft.ID must be registered in this scope;
//   - no new dep may be draft.ID itself or already reach it through the
//     registered dependency edges. Register can never form a cycle (a
//     DependsOn target is always registered first), but an amendment adds
//     edges to an old resource: e.g. an input that depends on an earlier
//     SystemdUnits composition (which contains the reload) merged into that
//     same reload, or that composition passed straight to FanIn/DependsOn.
//     Such a plan records fine but no host can apply it, so it is refused
//     here, at registration time;
//   - in record mode, the amend sink (SetPlanDraftAmender) must accept the
//     draft; it re-lowers it and replaces the recorded op.
//
// Only then are the edges added (the legacy repository apply order) and the
// stored draft replaced (the snapshot api.Apply lowers).
func AmendRegistered(draft PlanDraft, deps ...string) error {
	r := getRepository()
	res, err := r.checkAmendable(draft.ID, deps)
	if err != nil {
		return err
	}
	draftMu.Lock()
	amend := draftAmender
	draftMu.Unlock()
	if amend != nil {
		// Like RecordPlanDraft, the sink and the store each get their own
		// deep copy, so neither aliases the caller's draft or the other.
		if err := amend(draft.Clone()); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// dependsOn is a map shared by every copy of the registered Resource, so
	// the added edges are visible through the repository entry and through
	// values Register already handed out.
	for _, id := range deps {
		res.dependsOn[id] = struct{}{}
	}
	r.drafts[draft.ID] = draft.Clone()
	return nil
}

// checkAmendable returns the registered resource id after verifying that
// adding deps to it cannot close a dependency cycle (see AmendRegistered).
func (r *repository) checkAmendable(id string, deps []string) (Resource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.registered[id]
	if !ok {
		return Resource{}, fmt.Errorf("resource %s is not registered in this recipe scope", id)
	}
	// The direct self-dependency is checked first and worded on its own: it
	// arises when the new declaration names a composition that contains
	// id (SystemdUnits(FanIn(unitsA)), DaemonReload(DependsOn(unitsA))),
	// and "X already depends on X" would read as nonsense.
	for _, dep := range deps {
		if dep == id {
			return Resource{}, fmt.Errorf("the new declaration depends on %s itself (e.g. via FanIn or DependsOn of a composition containing it), so merging it would make %s depend on itself", id, id)
		}
	}
	for _, dep := range deps {
		if r.reaches(dep, id) {
			return Resource{}, fmt.Errorf("%s already depends on %s, so adding the dependency would form a cycle no host can apply", dep, id)
		}
	}
	return res, nil
}

// reaches reports whether target is reachable from start through the
// registered dependency edges. Unregistered ids (from an earlier scope)
// end the walk. The caller holds r.mu.
func (r *repository) reaches(start, target string) bool {
	seen := map[string]bool{}
	stack := []string{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		res, ok := r.registered[id]
		if !ok {
			continue
		}
		for dep := range res.dependsOn {
			if dep == target {
				return true
			}
			stack = append(stack, dep)
		}
	}
	return false
}
