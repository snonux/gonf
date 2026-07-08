package resource

import (
	"fmt"
	"log"
)

type Applier interface {
	Apply() error
}

type ApplierFunc func() error

func (f ApplierFunc) Apply() error {
	return f()
}

type Resource struct {
	Type      string
	Name      string
	applier   Applier
	dependsOn map[string]struct{}
}

func Register(type_, name string, apply Applier) Resource {
	r := Resource{
		Type:      type_,
		Name:      name,
		applier:   apply,
		dependsOn: make(map[string]struct{}),
	}

	if err := getRepository().register(r); err != nil {
		log.Fatalf("resource registration failed: %v", err)
	}

	return r
}

func (r Resource) String() string {
	return r.ID()
}

func (r Resource) ID() string {
	return fmt.Sprintf("%s[%s]", r.Type, r.Name)
}

func (r Resource) Apply() error {
	return r.applier.Apply()
}
