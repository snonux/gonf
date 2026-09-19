package systemd

import (
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler is the daemon_reload kind's plan.Handler: see
// resource/pkg/planwire.go for why record-time ToOp and apply-time Apply
// live together in the resource package instead of api/plan.go's
// draftToOp and plan/apply.go's applyDaemonReload.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindDaemonReload, planHandler{})
}

// ToOp lowers a "daemon_reload" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:        plan.KindDaemonReload,
		ID:        d.ID,
		User:      d.User,
		IfChanged: d.IfChanged,
		Watch:     d.Watch,
		Deps:      d.Deps,
	}, nil
}

// Apply runs systemctl daemon-reload, optionally gated on whether any
// watched resource changed, mirroring the resource's own option handling
// exactly.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	var opts []opt.DaemonReloadOption
	if op.User {
		opts = append(opts, opt.WithUser)
	}
	if op.IfChanged {
		opts = append(opts, opt.IfChanged)
		if len(op.Watch) > 0 {
			opts = append(opts, opt.WithWatch(op.Watch...))
		}
	}
	return Ensure(opts...)
}
