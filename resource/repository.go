package resource

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
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
	log.Printf("Registered resource %v\n", res)

	return nil
}

func (r *repository) apply() error {
	r.mu.Lock()
	defer r.mu.Unlock()

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
		// Visit dependencies in sorted order so the resulting apply order is
		// stable and the log output is reproducible.
		for _, depID := range res.sortedDependsOn() {
			log.Printf("Resolving dependency of %v: needs %s first", res, depID)
			if err := visit(depID); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		order = append(order, res)
		return nil
	}

	// Seed the traversal from a sorted list of roots so the overall order is
	// deterministic regardless of map iteration order.
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
	log.Printf("Resolved apply order: %s", strings.Join(orderIDs, " -> "))

	for _, res := range order {
		if deps := res.sortedDependsOn(); len(deps) > 0 {
			log.Printf("Applying resource %v (dependencies already applied: %s)",
				res, strings.Join(deps, ", "))
		} else {
			log.Printf("Applying resource %v (no dependencies)", res)
		}
		if err := res.Apply(); err != nil {
			return fmt.Errorf("failed to apply %v: %w", res, err)
		}
	}

	return nil
}

func Apply() error {
	return getRepository().apply()
}
