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
// watched resource changed since the bus's last reload in this apply
// (changedSinceLastReload), mirroring the resource's own option handling
// exactly. The gate is rebuilt by opt.RecordedChangeGate, the rule every
// gated kind shares: a gated op with no watch ids is an error (it could
// never reload), and an ungated op's recorded watch ids are ignored. The
// recorded watch list already includes the DependsOn fallback, so the
// rebuilt reload needs no deps.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	var opts []opt.DaemonReloadOption
	if op.User {
		opts = append(opts, opt.WithUser)
	}
	gate, err := opt.RecordedChangeGate(string(plan.KindDaemonReload), op.IfChanged, op.Watch)
	if err != nil {
		return err
	}
	if gate != nil {
		opts = append(opts, gate)
	}
	return Ensure(opts...)
}
