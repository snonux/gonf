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
