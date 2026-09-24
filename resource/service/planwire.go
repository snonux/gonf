package service

import (
	"fmt"
	"slices"

	"github.com/snonux/gonf/internal/runners"
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

// ToOp lowers a "service" resource draft to a plan.Op. Reload and the
// WithFlags flags come from d.Payload (Payload, task w62 Layer 1); a
// "service" draft without one is a record-time bug (planDraft always sets
// it), reported like any other handler error rather than panicking. Reload
// stays a flat plan.Op field, the flags travel in plan.ServicePayload
// (schema v25), which ToOp always sets, like every payload-carrying kind.
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
		Payload: plan.ServicePayload{Flags: p.Flags, HasFlags: p.HasFlags},
	}
	// Change gate (schema v11): OnChange arms IfChanged with the watched ids.
	if d.IfChanged {
		op.IfChanged = true
		op.Watch = slices.Clone(d.Watch)
	}
	return op, nil
}

// Apply starts/stops/enables/disables the named service, mirroring
// resource/service's own Present/Absent option handling. ctx.Runners.
// Service and .Systemd, when this apply had them injected (task 4e2; nil in
// every real apply), replace the real runners.
func (planHandler) Apply(op plan.Op, ctx plan.ApplyContext) error {
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
	// Managed flags (schema v25) rebuild WithFlags, empty flags included; a
	// decoded op without a ServicePayload reads as unmanaged flags.
	if p := plan.PayloadOf[plan.ServicePayload](op); p.HasFlags {
		opts = append(opts, opt.WithFlags(p.Flags))
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
	return EnsureWith(runners.ServiceOf(ctx.Runners), runners.SystemdOf(ctx.Runners), op.Name, opts...)
}
