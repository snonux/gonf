package resource

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/snonux/gonf/internal/logger"
)

var (
	repo repository
	once sync.Once
)

func getRepository() *repository {
	once.Do(func() {
		repo = newRepository()
	})
	return &repo
}

// ResetRepository swaps in a fresh empty repository. Tests use it to
// isolate registrations between runs.
func ResetRepository() {
	repo = newRepository()
}

type repository struct {
	registered map[string]Resource
	mu         sync.Mutex
}

func newRepository() repository {
	return repository{
		registered: make(map[string]Resource),
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
			return err
		}
	}

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

	PrintSummary(os.Stderr)
	return nil
}

// Apply applies every registered resource in dependency order: it
// topologically sorts the registration graph (rejecting cycles and dangling
// dependency IDs), applies each resource, and prints the outcome summary to
// stderr.
func Apply() error {
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
