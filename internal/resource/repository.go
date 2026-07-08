package resource

import (
	"fmt"
	"log"
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
		for depID := range res.dependsOn {
			if err := visit(depID); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		order = append(order, res)
		return nil
	}

	for id := range r.registered {
		if err := visit(id); err != nil {
			return err
		}
	}

	for _, res := range order {
		log.Printf("Applying resource %v", res)
		if err := res.Apply(); err != nil {
			return fmt.Errorf("failed to apply %v: %w", res, err)
		}
	}

	return nil
}

func Apply() error {
	return getRepository().apply()
}
