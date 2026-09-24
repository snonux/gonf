package noop

import (
	"errors"
	"slices"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// planHandler is the noop kind's plan.Handler: see resource/pkg/planwire.go
// for why record-time ToOp and apply-time Apply live together in the
// resource package.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindNoop, planHandler{})
}

// ToOp lowers a "noop" draft to a plan.Op carrying only its identity and
// deps (a noop has no payload of its own).
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:   plan.KindNoop,
		ID:   d.ID,
		Name: d.Name,
		Deps: slices.Clone(d.Deps),
	}, nil
}

// Apply notes the noop ok on the destination; a noop op without a name is
// refused like every kind's missing identity.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return errors.New("noop: missing name")
	}
	return Ensure(op.Name)
}
