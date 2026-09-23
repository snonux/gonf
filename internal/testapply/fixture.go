package testapply

import (
	"fmt"
	"slices"
	"sync"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// fixtureKind is the plan kind of a Register fixture. It is not a declared
// plan.Kind (plan.IsKnownKind is false), so a fixture can never reach a real
// plan file: api's packager refuses it, and only Apply here lowers it.
const fixtureKind plan.Kind = "testapply_fixture"

// fixtures maps a fixture's resource ID to the work its op runs. Entries are
// never removed; a later Register under the same ID (the next test, after
// resource.ResetRepository) replaces the work. Like the resource repository
// itself, fixtures are meant for tests that do not run in parallel.
var (
	fixtures     = map[string]func() error{}
	fixturesMu   sync.Mutex
	registerOnce sync.Once
)

// fixtureHandler is the plan.Handler of fixtureKind: ToOp carries the ID and
// deps, Apply runs the work Register stored for that ID.
type fixtureHandler struct{}

var _ plan.Handler = fixtureHandler{}

// Register is the plan-path replacement for registering a hand-written work
// function directly: it registers typeName[name] with deps and records a
// plan draft for it, so Apply runs work in dependency order like any other
// resource. Tests use it for a stand-in that only notes a status (e.g. a
// File[unit] reported changed) to drive change gates and the report.
//
// The fixture kind's handler is installed on the first call, so a test
// binary that never uses a fixture has no extra kind registered.
func Register(typeName, name string, work func() error, deps ...string) resource.Resource {
	registerOnce.Do(func() { plan.RegisterHandler(fixtureKind, fixtureHandler{}) })
	res, ok := resource.Register(typeName, name, work, deps...)
	if ok {
		fixturesMu.Lock()
		fixtures[res.ID()] = work
		fixturesMu.Unlock()
		resource.RecordPlanDraft(resource.PlanDraft{Kind: string(fixtureKind), ID: res.ID(), Deps: slices.Clone(deps)})
	}
	return res
}

// Noting returns fixture work that notes each id with status st, the common
// Register body.
func Noting(st resource.Status, ids ...string) func() error {
	return func() error {
		for _, id := range ids {
			resource.Note(id, st)
		}
		return nil
	}
}

// ToOp lowers a fixture draft to its op.
func (fixtureHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{Op: fixtureKind, ID: d.ID, Deps: slices.Clone(d.Deps)}, nil
}

// Apply runs the work Register stored for op.ID.
func (fixtureHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	fixturesMu.Lock()
	work, ok := fixtures[op.ID]
	fixturesMu.Unlock()
	if !ok {
		return fmt.Errorf("testapply: no fixture registered for %s", op.ID)
	}
	return work()
}
