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
// name the instance name, registered whatever the kind wants Registered to
// hand back later — usually its concrete resource, e.g. *file.File; nil is
// fine for a kind that never reads it back. The value is stored untyped and
// unconstrained (not necessarily a pointer: resource/configset's
// ConfigSetMember passes a value copy of its member spec, since memberSpec
// has no methods of its own) — nothing applies it through the repository
// any more, see Registered — and deps the IDs of resources that must be
// applied first. Register records no plan draft itself: the kind's
// Present records one with RecordPlanDraft, and api.Apply refuses a
// registered resource without a draft (it would otherwise be silently
// skipped). A duplicate ID (the same Type[Name] registered twice in one
// recipe scope) is always a task bug: it is reported as a declaration error
// (internal/declerr, surfaced by RecordPlan, Run, Apply and the CLI) and the
// second declaration is not registered; its Resource value is still
// returned so the recipe keeps running up to the point where the error
// surfaces.
//
// ok reports whether THIS call actually added the entry: false on the
// duplicate-ID case above. A caller that goes on to record a plan draft for
// the returned Resource (RecordPlanDraft) MUST skip that call when ok is
// false — every Present-style constructor in the module follows this
// `r, ok := resource.Register(...); if ok { resource.RecordPlanDraft(...) }`
// shape. Before task sf2 added ok, every one of those constructors called
// RecordPlanDraft unconditionally: repository.recordDraft only checked that
// the ID was PRESENT in the registered map, not which call put it there, so
// a refused (colliding) second declaration's draft silently overwrote the
// first, successfully registered declaration's draft under the same ID —
// the repository ended up with Registered() answering with the FIRST
// declaration's value but RegisteredPlanDrafts() answering with the SECOND,
// refused declaration's draft, a silent mismatch between what is registered
// and what actually gets applied. ok closes that gap by making the
// success/failure of this specific call explicit at the call site (the
// Go comma-ok idiom) instead of asking RecordPlanDraft to infer it later
// from repository state it cannot attribute to one call or the other.
//
// Registration is deliberately single-goroutine: recipe construction happens
// before fleet fan-out, so the repository is not safe for concurrent
// registration (see resource/repository.go).
func Register(type_, name string, registered any, deps ...string) (r Resource, ok bool) {
	dependsOn := make(map[string]struct{}, len(deps))
	for _, id := range deps {
		dependsOn[id] = struct{}{}
	}

	r = Resource{
		Type:       type_,
		Name:       name,
		registered: registered,
		dependsOn:  dependsOn,
	}

	err := getRepository().register(r)
	if err != nil {
		declerr.Reportf("resource registration failed: %w", err)
	}

	return r, err == nil
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

// ResetDeclarationError clears the sticky declaration error that Register or
// Refuse leaves behind once internal/declerr's first report is made outside
// a RecordPlanTo recording (see AGENTS.md's "Registration-time contract" and
// api.Apply's own doc comment) — a duplicate/collided resource ID (including
// the WhenHostname/WhenPathExists direct-apply collision resource.Register's
// declerr.Reportf reports, task vd2) or any other DSL misuse Refuse
// reported. That report is deliberately sticky for the process (a later,
// unrelated registration must not silently mask it — task ad2/id2's
// reasoning applies here too, see ResetDeclarationError's discussion in
// AGENTS.md), so api.Apply, api.RecordPlanTo and api.Run keep refusing with
// it until it is cleared. resource.ResetForTest already clears it for
// tests, but it is documented and scoped as a test seam and also wipes the
// registered repository, its drafts, the apply report and dry-run —
// unsuitable for production code. ResetDeclarationError is the
// production-safe, single-purpose equivalent (task oe2): a library embedder
// that confirmed the underlying recipe issue is fixed calls this to keep
// applying in the same process, without discarding anything else it does
// not need to. It clears only the sticky first error (internal/declerr.
// ResetFirst, task tf2) and never the capture sink an active RecordPlanTo
// recording installs, so calling it mid-recording cannot cause a later
// report in the same task body to miss that recording's capture and land
// back on the sticky slot instead — see ResetFirst's own doc comment for the
// silent-empty-secret-written bug that shape used to cause. Even so, this
// function's documented, supported use is the direct-apply path, not while
// a recording is active; a caller that must clear it mid-recording anyway
// should also confirm no report happens between the call and the body's
// return, since a report the sink would have caught is still visible only
// through the returned error below, not automatically retried against the
// recording.
//
// ResetDeclarationError returns the error it discarded (task vf2), or nil
// when nothing was pending, so a caller that clears it must look at what it
// is discarding rather than silently moving on. NOT every declaration error
// class is safe to clear and continue from: a collided resource ID
// (WhenHostname/WhenPathExists, or a plain duplicate Present/Register call)
// is safe, because nothing about the colliding registration attempt itself
// changed any OTHER resource's registered state — the recipe's earlier,
// successful registrations are exactly as declared. That "nothing else
// changed" half of the claim holds only for a LEAF resource, whose one
// Register call is the whole declaration. For a COMPOSITE resource — one
// whose registering constructor makes several resource.Register calls for a
// single recipe declaration, such as configset.Present registering the set
// plus one handle per member — the same colliding attempt can still let its
// OTHER calls succeed and register unless the constructor explicitly guards
// them on the first call's ok: a refused set whose member loop is not gated
// on the set's own ok would leave the refused declaration's members
// registered with a draft, which a subsequent apply then finds with no
// applied set to attach to (task ig2, confirmed by probe against
// configset.go before its member loop was gated this way). Present
// constructors are audited to hold this invariant (see
// resource/configset/configset.go's Present), so ResetDeclarationError's
// clear-and-continue collision remedy is safe fleet-wide today — but that
// safety comes from each composite's own internal guard, not for free from
// this function, and a new composite resource must gate every one of its
// extra Register calls on the outer call's ok the same way. A failed
// MustSecret/OptionalSecret/ResolveSecret lookup is NOT safe to clear and
// continue from: MustSecret cannot return an error, so a recipe that calls
// it inline (e.g. WithContent("password="+MustSecret(...))) has ALREADY
// registered that resource by the time the failure is reported, holding the
// function's inert zero return ("") where the real secret value belongs.
// Clearing the sticky error and simply continuing (the collision remedy)
// leaves that resource registered with the wrong value; the safe remedy for
// this class is to also call resource.ResetRepository() after clearing the
// error, so every resource is re-declared from scratch against a recipe
// that (having fixed whatever made the secret lookup fail) will now resolve
// it correctly the second time. ResetDeclarationError cannot tell these two
// classes apart on its own — internal/declerr carries every declaration
// error, of any cause, through the same one sticky slot — so it makes no
// attempt to; returning the discarded error is what lets the caller make
// that judgment instead of the mistake happening silently. See AGENTS.md's
// "Registration-time contract" and api.Apply's own doc comment for the same
// warning.
//
// Like every other repository primitive this is single-goroutine: call it
// only while no registration, recording, or apply is in flight.
func ResetDeclarationError() error {
	err := declerr.First()
	declerr.ResetFirst()
	return err
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
