package pkg

import (
	"fmt"
	"maps"
	"slices"

	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler is the package kind's plan.Handler: it owns both directions of
// the wire form (record-time ToOp, apply-time Apply) in one place, so a new
// Package option (like the IsLatest field this replaces the drift risk for
// — see task m5) is wired end to end by editing this one file instead of
// draftToOp in api/packager.go plus a separate applyPackage in plan/apply.go.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindPackage, planHandler{})
}

// ToOp lowers a "package" resource draft to a plan.Op. Latest comes from
// d.Payload (Payload, task w62 Layer 1) and is set on plan.PackagePayload
// (task 5e2 Layer 2); a "package" draft without one is a record-time bug
// (planDraft always sets it), reported like any other handler error rather
// than panicking.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(Payload)
	if !ok {
		return plan.Op{}, fmt.Errorf("package: draft missing pkg.Payload (got %T)", d.Payload)
	}
	return plan.Op{
		Op:      plan.KindPackage,
		ID:      d.ID,
		Name:    d.Name,
		Absent:  d.Absent,
		Env:     maps.Clone(d.Env),
		Deps:    slices.Clone(d.Deps),
		Payload: plan.PackagePayload{Latest: p.Latest},
	}, nil
}

// Apply installs, upgrades, or removes the named package, mirroring the
// resource's own Present/Absent/IsLatest option handling exactly (it calls
// the same Ensure entry point a direct, non-plan use would, through
// EnsureWith). ctx.Runners.Package, when this apply had one injected (task
// fg2), fakes the package-manager runners and detector for this apply only;
// nil (every production apply) uses the real ones.
func (planHandler) Apply(op plan.Op, ctx plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("package: missing name")
	}
	// p reads as the zero PackagePayload for a decoded or mistyped Payload
	// (see plan.PayloadOf's doc comment) — Latest reads as false, a clean
	// plain-install fallback.
	p := plan.PayloadOf[plan.PackagePayload](op)
	var opts []opt.PackageOption
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if p.Latest {
		opts = append(opts, opt.IsLatest)
	}
	if op.Env != nil {
		opts = append(opts, opt.WithEnv(op.Env))
	}
	// A sensitive op (scan-detected or WithSensitive at record time)
	// rebuilds a sensitive Package, which withholds failure output.
	if op.Sensitive {
		opts = append(opts, opt.WithSensitive)
	}
	return EnsureWith(runners.PackageOf(ctx.Runners), op.Name, opts...)
}
