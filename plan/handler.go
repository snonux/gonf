package plan

import (
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/resource"
)

// ApplyContext carries the apply-time inputs a Handler needs beyond the Op
// itself. It exists so Handler.Apply signatures do not have to grow a new
// parameter every time a future kind needs more context (mirroring why Op
// and PlanDraft are structs rather than positional arguments).
type ApplyContext struct {
	// PlanDir is the directory containing blobs/ sidecars (usually next to
	// the plan JSONL). Empty when the plan only uses content_b64 and no
	// blobs.
	PlanDir string
	// Facts are detected on the destination and exposed to template handlers.
	Facts Facts
	// Runners overrides the backend runners a migrated kind's Handler.Apply
	// uses in place of the real ones (internal/exec), for this apply only.
	// It is nil in a real apply (every handler then falls back to its real
	// runner) and is populated from the context ApplyWithContext was called
	// with (see internal/runners.WithSet/FromContext): a Runners set is
	// never a package-global, only a context value scoped to one apply. Its
	// type lives under internal/, so an external recipe module can read
	// this field but can never populate one with anything but nil.
	Runners *runners.Set
}

// Handler is a resource kind's ownership of its plan wire form: converting a
// package-neutral resource.PlanDraft into a plan.Op (record time) and
// applying a decoded plan.Op (apply time). Every resource kind implements
// Handler once and registers it for its Kind via RegisterHandler (usually
// from an init() in the resource package), instead of api/plan.go's
// draftToOp and plan/apply.go's applyActive each carrying a hand-written
// case for that kind. This is also what keeps this package free of
// resource-KIND imports: applyActive only ever calls through this interface,
// never a concrete resource kind. The packages are layered, not cyclic:
// plan imports only the kind-neutral core, resource (PlanDraft, the apply
// report) and resource/options (the option constructors OwnerGroupOptions
// and GuardOptions build), while every resource/<kind> backend imports plan
// to register its Handler, so plan must never import a resource/<kind>
// package (that would be an import cycle; TestPlanImportsOnlyResourceCore
// pins the rule).
//
// This does not change the wire format: Op and resource.PlanDraft stay the
// same flat structs (same JSON tags, same CurrentVersion) — only which Go
// code owns filling and reading them moves into the resource package that
// actually knows what its fields mean.
type Handler interface {
	// ToOp lowers a resource draft to a plan Op line. The returned Op's Op
	// field (the Kind discriminator) must be set by the implementation.
	//
	// The Op must not alias d: every slice, map or pointer the Op takes
	// from d is copied (slices.Clone / maps.Clone for slices and maps, a
	// fresh pointee for pointers; nil stays nil in every case), so a
	// later change to the draft cannot change the op or vice versa. This
	// is the op half of the copy contract whose draft half is
	// resource.PlanDraft.Clone; TestHandlersToOpDoNotAliasDraft (api)
	// checks every registered kind.
	ToOp(d resource.PlanDraft) (Op, error)
	// Apply performs this op's kind-specific side effect. It must validate
	// required fields itself (mirroring the hand-written applyX handlers)
	// before any mutation.
	Apply(op Op, ctx ApplyContext) error
}

// handlers maps every Kind to the resource package's Handler. Every resource
// kind registers itself here from its own package's init() (see
// docs/plan.md, "Adding a resource kind"); api/packager.go's draftToOp and
// plan/apply.go's applyActive both fail loudly on an unmapped Kind instead of
// carrying a fallback case, so a new kind that forgets to register is caught
// immediately instead of silently falling through to dead code.
var handlers = map[Kind]Handler{}

// RegisterHandler installs h as the plan wire-form owner for k. Intended to
// be called from a resource package's init(), so importing that package for
// its normal DSL entry points (e.g. api importing resource/pkg for
// api.Package) is what wires the handler in — no separate registration step
// to forget. Panics on a duplicate registration for the same Kind: that is a
// programming error (two packages claiming the same wire kind), not a
// runtime condition to recover from.
func RegisterHandler(k Kind, h Handler) {
	if _, dup := handlers[k]; dup {
		panic("plan: duplicate Handler registration for kind " + string(k))
	}
	handlers[k] = h
}

// HandlerFor returns the registered Handler for k, if any.
func HandlerFor(k Kind) (Handler, bool) {
	h, ok := handlers[k]
	return h, ok
}
