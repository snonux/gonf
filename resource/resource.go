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

// Applier is the idempotent work a registered resource performs during
// Apply. Concrete resource types hand an ApplierFunc to Register.
type Applier interface {
	Apply() error
}

// ApplierFunc adapts a plain function to the Applier interface so resource
// packages can register closures without declaring a named type.
type ApplierFunc func() error

// Apply runs the wrapped function.
func (f ApplierFunc) Apply() error {
	return f()
}

// Resource is the value returned by the DSL constructors (api.File,
// api.Dir, ...). It identifies the registered resource and can be passed to
// the DependsOn option. The actual OS work stays behind the unexported
// applier so this package does not depend on concrete resource types.
type Resource struct {
	// Type is the resource kind label, e.g. "File" or "Directory".
	Type string
	// Name identifies the instance within its Type, e.g. a path.
	Name      string
	applier   Applier
	dependsOn map[string]struct{}
}

// Register records a resource in the repository so Apply runs it after its
// dependencies. type_ is the kind label, name the instance name, apply the
// idempotent work, and deps the IDs of resources that must be applied first.
// It exits via logger.Fatal on a duplicate ID: registering the same
// Type[Name] twice is always a task bug.
//
// Registration is deliberately single-goroutine: recipe construction happens
// before fleet fan-out, so the repository is not safe for concurrent
// registration (see resource/repository.go).
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

// String returns the resource's ID.
func (r Resource) String() string {
	return r.ID()
}

// ID returns the unique "Type[Name]" identifier used for dependency edges
// and report notes.
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

// Apply runs this resource's idempotent work. The repository calls it only
// after every dependency has been applied.
func (r Resource) Apply() error {
	return r.applier.Apply()
}
