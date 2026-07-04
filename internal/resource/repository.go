package resource

import (
	"fmt"
	"log"
	"sync"

	"codeberg.org/snonux/gonf/internal/resource"
)

var (
	repo repository
	once sync.Once
)

func initRepository() {
	once.Do(func() {
		repo = newRepository()
	})
}

type repository struct {
	registered map[string]resource.Resource
	mu         *sync.Mutex
}

func newRepository() repository {
	return repository{
		registered: make(map[string]resource.Resource),
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

// func (r repository)(res Resource) error {
// 	return nil
// }
