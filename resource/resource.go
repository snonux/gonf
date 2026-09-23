// Package resource defines gonf's core resource abstraction: the Resource
// type, the repository that records registered resources and their plan
// drafts (applied by the plan engine through api.Apply and api.Run), the
// apply report, and shared helpers such as Multi. Concrete resource kinds
// (file, dir, link, pkg, cmd, ...) live in subpackages.
package resource

import (
	"sort"

	"github.com/snonux/gonf/internal/declerr"
)

// Resource is the value returned by the DSL constructors (api.File,
// api.Dir, ...). It identifies the registered resource and can be passed to
// the DependsOn option. It keeps the registered value (see Register) for
// Registered and its dependency edges for AmendRegistered's cycle check;
// applying uses the registered plan draft instead.
type Resource struct {
	// Type is the resource kind label, e.g. "File" or "Directory".
	Type string
	// Name identifies the instance within its Type, e.g. a path.
	Name       string
	registered any
	dependsOn  map[string]struct{}
}

// Register records a resource in the repository. type_ is the kind label,
// name the instance name, registered the kind's registered value (the
// concrete resource, e.g. *file.File — nothing applies it through the
// repository any more, see Registered), and deps the IDs of resources that
// must be applied first. Register records no plan draft itself: the kind's
// Present records one with RecordPlanDraft, and api.Apply refuses a
// registered resource without a draft (it would otherwise be silently
// skipped). A duplicate ID (the same Type[Name] registered twice in one
// recipe scope) is always a task bug: it is reported as a declaration error
// (internal/declerr, surfaced by RecordPlan, Run, Apply and the CLI) and the
// second declaration is not registered; its Resource value is still
// returned so the recipe keeps running up to the point where the error
// surfaces.
//
// Registration is deliberately single-goroutine: recipe construction happens
// before fleet fan-out, so the repository is not safe for concurrent
// registration (see resource/repository.go).
func Register(type_, name string, registered any, deps ...string) Resource {
	dependsOn := make(map[string]struct{}, len(deps))
	for _, id := range deps {
		dependsOn[id] = struct{}{}
	}

	r := Resource{
		Type:       type_,
		Name:       name,
		registered: registered,
		dependsOn:  dependsOn,
	}

	if err := getRepository().register(r); err != nil {
		declerr.Reportf("resource registration failed: %w", err)
	}

	return r
}

// Refuse reports err as the declaration error of a resource that a
// registering constructor (Present, Absent, ...) could not build — invalid
// options, an option misuse collected by embed.Misuse — and returns the
// unregistered Resource value type_[name] in place of a registered one. The
// recipe keeps running (so later declarations are still checked), and the
// error surfaces from RecordPlan, Run, Apply and the CLI (internal/declerr).
// Nothing is registered and no plan draft is recorded for it.
func Refuse(type_, name string, err error) Resource {
	declerr.Report(err)
	return Resource{Type: type_, Name: name}
}

// String returns the resource's ID.
func (r Resource) String() string {
	return r.ID()
}

// ID returns the unique "Type[Name]" identifier used for dependency edges
// and report notes.
func (r Resource) ID() string {
	return FormatID(r.Type, r.Name)
}

// FormatID is the single definition of the "Type[Name]" identifier format.
// Resource.ID uses it, and so does every other place in gonf that builds a
// full ID to report, gate, or reference a resource before (or without)
// registering it (a backend's Mutate notes, dependency and watch IDs, an
// ID-only requirement block), so that AnyChanged and NoteResult always match
// the registered spelling. The report's directory matching parses IDs back
// apart; it builds its prefixes with idPrefix, the other half of this
// format. Tests keep literal IDs on purpose: they pin the wire spelling.
func FormatID(typeName, name string) string {
	return idPrefix(typeName) + name + "]"
}

// idPrefix is the "Type[" start of every ID of typeName, for prefix matching
// and parsing IDs built by FormatID.
func idPrefix(typeName string) string { return typeName + "[" }

// Dependencies returns this resource's own ID. It lets a single Resource be
// used as a DependsOn target, mirroring Multi.Dependencies.
func (r Resource) Dependencies() []string {
	return []string{r.ID()}
}

// sortedDependsOn returns the IDs this resource depends on, sorted, so tests
// can compare the registered dependency edges.
func (r Resource) sortedDependsOn() []string {
	ids := make([]string, 0, len(r.dependsOn))
	for id := range r.dependsOn {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
