package resources

import (
	"fmt"
	"log"
	"sync"

	"codeberg.org/snonux/gonf/internal/resource"
)

var (
	registry resources
	once     sync.Once
	mu       sync.Mutex
)

func Init() {
	once.Do(func() {
		registry = new()
	})
}

type resources struct {
	registered map[string]resource.Resource
}

func new() resources {
	return resources{
		registered: make(map[string]resource.Resource),
	}
}

func Register(res resource.Resource) error {
	mu.Lock()
	defer mu.Unlock()

	if _, exists := registry.registered[res.ID()]; exists {
		return fmt.Errorf("resource %v already registered", res)
	}

	registry.registered[res.ID()] = res
	log.Printf("Registered resource %v\n", res)

	return nil
}
