package resource

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/snonux/gonf/internal/declerr"
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

func (r *repository) apply() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ResetReport()
	order, err := r.resolveApplyOrder()
	if err != nil {
		return err
	}
	if err := r.applyResources(order); err != nil {
		return err
	}
	PrintSummary(os.Stderr)
	return nil
}

func (r *repository) resolveApplyOrder() ([]Resource, error) {
	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	var order []Resource

	var visit func(id string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("circular dependency detected involving %s", id)
		}
		if visited[id] {
			return nil
		}

		res, ok := r.registered[id]
		if !ok {
			return fmt.Errorf("resource %s is depended upon but not registered", id)
		}

		visiting[id] = true
		for _, depID := range res.sortedDependsOn() {
			logger.Debug("Resolving dependency of %v: needs %s first", res, depID)
			if err := visit(depID); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		order = append(order, res)
		return nil
	}

	roots := make([]string, 0, len(r.registered))
	for id := range r.registered {
		roots = append(roots, id)
	}
	sort.Strings(roots)

	for _, id := range roots {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func (r *repository) applyResources(order []Resource) error {
	orderIDs := make([]string, 0, len(order))
	for _, res := range order {
		orderIDs = append(orderIDs, res.ID())
	}
	logger.Debug("Resolved apply order: %s", strings.Join(orderIDs, " -> "))

	for _, res := range order {
		if deps := res.sortedDependsOn(); len(deps) > 0 {
			logger.Debug("Applying resource %v (dependencies already applied: %s)",
				res, strings.Join(deps, ", "))
		} else {
			logger.Debug("Applying resource %v (no dependencies)", res)
		}
		if err := res.Apply(); err != nil {
			return fmt.Errorf("failed to apply %v: %w", res, err)
		}
	}
	return nil
}

// Apply applies every registered resource through the legacy direct path. It
// topologically sorts the registration graph (rejecting cycles and dangling
// dependency IDs), applies each resource, and prints the outcome summary to
// stderr.
//
// A declaration error reported earlier (internal/declerr: DSL misuse outside
// a plan recording) refuses the apply before anything runs.
//
// Prefer api.Apply or api.Run, which use the plan engine.
func Apply() error {
	if err := declerr.First(); err != nil {
		return err
	}
	return getRepository().apply()
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
