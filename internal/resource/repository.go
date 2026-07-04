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

func getRepository() repository {
	once.Do(func() {
		repo = newRepository()
	})
	return repo
}

func resetRepository() {
	repo = newRepository()
}

type repository struct {
	registered map[string]Resource
	mu         *sync.Mutex
}

func newRepository() repository {
	return repository{
		registered: make(map[string]Resource),
		mu:         new(sync.Mutex),
	}
}

func (r repository) register(res Resource) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.registered[res.ID()]; exists {
		return fmt.Errorf("resource %v already registered", res)
	}

	r.registered[res.ID()] = res
	log.Printf("Registered resource %v\n", res)

	return nil
}
