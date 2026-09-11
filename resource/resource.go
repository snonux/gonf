// Package resource defines gonf's core resource abstraction: the Resource
// type, the repository that applies registered resources in dependency
// order, and shared helpers such as Multi. Concrete resource kinds (file,
// dir, link, pkg, cmd) live in subpackages.
package resource

import (
	"fmt"
	"sort"

	"github.com/snonux/gonf/internal/logger"
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

func Register(type_, name string, apply Applier, deps ...string) Resource {
	dependsOn := make(map[string]struct{}, len(deps))
	for _, id := range deps {
		dependsOn[id] = struct{}{}
	}

	r := Resource{
		Type:      type_,
		Name:      name,
		applier:   apply,
		dependsOn: dependsOn,
	}

	if err := getRepository().register(r); err != nil {
		logger.Fatal("resource registration failed: %v", err)
	}

	return r
}

func (r Resource) String() string {
	return r.ID()
}

func (r Resource) ID() string {
	return fmt.Sprintf("%s[%s]", r.Type, r.Name)
}

// Dependencies returns this resource's own ID. It lets a single Resource be
// used as a DependsOn target, mirroring Multi.Dependencies.
func (r Resource) Dependencies() []string {
	return []string{r.ID()}
}

// sortedDependsOn returns the IDs this resource depends on, sorted for stable
// and readable log output.
func (r Resource) sortedDependsOn() []string {
	ids := make([]string, 0, len(r.dependsOn))
	for id := range r.dependsOn {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (r Resource) Apply() error {
	return r.applier.Apply()
}
