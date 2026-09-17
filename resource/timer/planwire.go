package timer

import (
	"fmt"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// planHandler is the timer kind's plan.Handler: see resource/pkg/planwire.go
// for why record-time ToOp and apply-time Apply live together in the
// resource package instead of api/plan.go's draftToOp and plan/apply.go's
// applyTimer.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindTimer, planHandler{})
}

// ToOp lowers a "timer" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:         plan.KindTimer,
		ID:         d.ID,
		Name:       d.Name,
		Absent:     d.Absent,
		User:       d.User,
		Restart:    d.Restart,
		EnableOnly: d.EnableOnly,
		Deps:       d.Deps,
	}, nil
}

// Apply enables/starts, restarts, or stops/disables the named systemd timer
// unit, mirroring the resource's own option handling exactly.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("timer: missing name")
	}
	var opts []opt.Option
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.User {
		opts = append(opts, opt.WithUser)
	}
	if op.Restart {
		opts = append(opts, opt.WithRestart)
	}
	if op.EnableOnly {
		opts = append(opts, opt.WithEnableOnly)
	}
	return Ensure(op.Name, opts...)
}
