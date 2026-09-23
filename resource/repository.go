package resource

import (
	"fmt"
	"sort"
	"sync"

	"github.com/snonux/gonf/internal/logger"
)

// The DSL registries are deliberately single-goroutine: recipe construction
// happens before fleet fan-out (PushCluster records centrally, then streams
// the same bytes over SSH), so nothing in this file is safe for concurrent
// registration or reset. repoMu guards only the repo pointer itself
// (getRepository reads, ResetRepository swaps); repository.mu guards the
// contents of one repository instance.
var (
	repo   *repository
	repoMu sync.Mutex
)

func getRepository() *repository {
	repoMu.Lock()
	defer repoMu.Unlock()
	if repo == nil {
		repo = newRepository()
	}
	return repo
}

// ResetRepository swaps in a fresh empty repository. Tests use it to
// isolate registrations between runs; resource.ResetForTest is the
// canonical single entry point for resetting all resource state.
//
// The swap takes repoMu so it cannot race a concurrent getRepository read
// handing out the previous pointer. Callers keep their snapshot (the old
// repository stays valid) until they re-read getRepository; registration
// and apply remain single-goroutine by DSL invariant.
func ResetRepository() {
	repoMu.Lock()
	defer repoMu.Unlock()
	repo = newRepository()
}

// SnapshotRepository swaps in a fresh empty repository and returns a
// restore func that swaps the ORIGINAL one back, discarding whatever the
// fresh one accumulated in between. api.RecordPlanTo uses it (tasks
// ad2/bd2, id2) to undo exactly what one record attempt itself registered
// — on every outcome (success, failure, or a recovered panic), since the
// record's own findings are already captured in its returned ops (or lost
// with its error) by the time it returns, nothing about its own
// registrations needs to survive in the live repository — while leaving
// whatever was registered BEFORE that attempt started untouched, verbatim.
//
// This replaced an earlier, ID-based RollbackTo(kept []string) (tasks
// ad2/bd2): pruning down to a set of ID names, rather than restoring the
// actual pre-snapshot values, meant a resource the record attempt
// registered under an ID that collided with one from BEFORE the snapshot
// was silently KEPT (with the record's own, new value) instead of the
// original being restored — reopening the exact when-guard-bypass bd2
// fixed, just one ID collision away (task id2). Swapping the whole
// repository pointer back cannot have that failure mode: there is no
// per-ID merge decision to get wrong.
//
// Single-goroutine by the same DSL invariant every other repository
// primitive relies on: nothing may register concurrently with a snapshot
// still outstanding, and only one snapshot may be outstanding at a time
// (restoring an outer one after an inner one already restored would lose
// the inner scope's own restore).
func SnapshotRepository() (restore func()) {
	repoMu.Lock()
	saved := repo
	if saved == nil {
		saved = newRepository()
	}
	repo = newRepository()
	repoMu.Unlock()
	return func() {
		repoMu.Lock()
		repo = saved
		repoMu.Unlock()
	}
}

// repository is one recipe scope's registry: the registered resources (by
// ID, with their dependency edges and registered value) and the plan draft
// each one recorded. It applies nothing itself; api.Apply and api.Run lower
// the drafts to plan ops and apply them through the plan engine (the direct
// repository apply path was retired in task e72).
type repository struct {
	registered map[string]Resource
	drafts     map[string]PlanDraft
	mu         sync.Mutex
}

func newRepository() *repository {
	return &repository{
		registered: make(map[string]Resource),
		drafts:     make(map[string]PlanDraft),
	}
}

func (r *repository) register(res Resource) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.registered[res.ID()]; exists {
		return fmt.Errorf("resource %v already registered", res)
	}

	r.registered[res.ID()] = res
	logger.Debug("Registered resource %v", res)

	return nil
}

// RegisteredIDs returns the sorted IDs of all currently registered resources.
// The plan recorder uses it to fail loudly when a registered resource kind
// produced no plan draft (it would otherwise be silently skipped by apply).
func RegisteredIDs() []string {
	return getRepository().registeredIDs()
}

func (r *repository) registeredIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]string, 0, len(r.registered))
	for id := range r.registered {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// recordDraft stores draft for its registered resource. The caller hands
// over ownership: RecordPlanDraft passes a fresh PlanDraft.Clone.
func (r *repository) recordDraft(draft PlanDraft) {
	if draft.ID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, registered := r.registered[draft.ID]; !registered {
		return
	}
	r.drafts[draft.ID] = draft
}

// draftsSnapshot returns deep copies (PlanDraft.Clone) of the stored drafts,
// sorted by resource ID, so a caller mutating the snapshot cannot reach into
// the store.
func (r *repository) draftsSnapshot() []PlanDraft {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]string, 0, len(r.drafts))
	for id := range r.drafts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	drafts := make([]PlanDraft, 0, len(ids))
	for _, id := range ids {
		drafts = append(drafts, r.drafts[id].Clone())
	}
	return drafts
}
