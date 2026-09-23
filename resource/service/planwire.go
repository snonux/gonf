package service

import (
	"fmt"
	"slices"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler is the service kind's plan.Handler: see
// resource/pkg/planwire.go for why record-time ToOp and apply-time Apply
// live together in the resource package instead of api/plan.go's
// draftToOp and plan/apply.go's applyService.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindService, planHandler{})
}

// ToOp lowers a "service" resource draft to a plan.Op. Reload comes from
// d.Payload (Payload, task w62 Layer 1); a "service" draft without one is a
// record-time bug (planDraft always sets it), reported like any other
// handler error rather than panicking.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(Payload)
	if !ok {
		return plan.Op{}, fmt.Errorf("service: draft missing service.Payload (got %T)", d.Payload)
	}
	op := plan.Op{
		Op:      plan.KindService,
		ID:      d.ID,
		Name:    d.Name,
		Absent:  d.Absent,
		Restart: d.Restart,
		Reload:  p.Reload,
		User:    d.User,
		Deps:    slices.Clone(d.Deps),
	}
	// Change gate (schema v11): OnChange arms IfChanged with the watched ids.
	if d.IfChanged {
		op.IfChanged = true
		op.Watch = slices.Clone(d.Watch)
	}
	return op, nil
}

// Apply starts/stops/enables/disables the named service, mirroring
// resource/service's own Present/Absent option handling.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("service: missing name")
	}
	var opts []opt.ServiceOption
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.Restart {
		opts = append(opts, opt.WithRestart)
	}
	if op.Reload {
		opts = append(opts, opt.WithReload)
	}
	if op.User {
		opts = append(opts, opt.WithUser)
	}
	// The recorded gate is rebuilt by the rule every gated kind shares
	// (opt.RecordedChangeGate): a gated op without watch ids is an error.
	gate, err := opt.RecordedChangeGate(string(plan.KindService), op.IfChanged, op.Watch)
	if err != nil {
		return err
	}
	if gate != nil {
		opts = append(opts, gate)
	}
	return Ensure(op.Name, opts...)
}
