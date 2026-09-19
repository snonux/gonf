package service

import (
	"fmt"

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

// ToOp lowers a "service" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	op := plan.Op{
		Op:      plan.KindService,
		ID:      d.ID,
		Name:    d.Name,
		Absent:  d.Absent,
		Restart: d.Restart,
		Reload:  d.Reload,
		User:    d.User,
		Deps:    d.Deps,
	}
	// Change gate (schema v11): OnChange arms IfChanged with the watched ids.
	if d.IfChanged {
		op.IfChanged = true
		op.Watch = append([]string(nil), d.Watch...)
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
	if op.IfChanged {
		if len(op.Watch) == 0 {
			return fmt.Errorf("service: if_changed without watch ids")
		}
		opts = append(opts, opt.WatchChanges(op.Watch...))
	}
	return Ensure(op.Name, opts...)
}
