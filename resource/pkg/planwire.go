package pkg

import (
	"fmt"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// planHandler is the package kind's plan.Handler: it owns both directions of
// the wire form (record-time ToOp, apply-time Apply) in one place, so a new
// Package option (like the IsLatest field this replaces the drift risk for
// — see task m5) is wired end to end by editing this one file instead of
// draftToOp in api/plan.go plus a separate applyPackage in plan/apply.go.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindPackage, planHandler{})
}

// ToOp lowers a "package" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:     plan.KindPackage,
		ID:     d.ID,
		Name:   d.Name,
		Absent: d.Absent,
		Latest: d.Latest,
		Deps:   d.Deps,
	}, nil
}

// Apply installs, upgrades, or removes the named package, mirroring the
// resource's own Present/Absent/IsLatest option handling exactly (it calls
// the same Ensure entry point a direct, non-plan use would).
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("package: missing name")
	}
	var opts []opt.Option
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.Latest {
		opts = append(opts, opt.IsLatest)
	}
	return Ensure(op.Name, opts...)
}
