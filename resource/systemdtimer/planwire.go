package systemdtimer

import (
	"fmt"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler is the systemd_timer kind's plan.Handler: see
// resource/pkg/planwire.go for why record-time ToOp and apply-time Apply
// live together in the resource package instead of api/plan.go's
// draftToOp and plan/apply.go's applySystemdTimer.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindSystemdTimer, planHandler{})
}

// ToOp lowers a "systemd_timer" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:                 plan.KindSystemdTimer,
		ID:                 d.ID,
		Name:               d.Name,
		Absent:             d.Absent,
		User:               d.User,
		Restart:            d.Restart,
		EnableOnly:         d.EnableOnly,
		Command:            d.Command,
		OnCalendar:         d.OnCalendar,
		OnBootSec:          d.OnBootSec,
		Persistent:         d.Persistent,
		Description:        d.Description,
		ServiceDescription: d.ServiceDescription,
		After:              d.After,
		Wants:              d.Wants,
		Deps:               d.Deps,
	}, nil
}

// Apply installs/enables/starts, restarts, or stops/disables/removes the
// declarative timer + oneshot service pair, mirroring the resource's own
// option handling exactly.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("systemd_timer: missing name")
	}
	var opts []opt.SystemdTimerOption
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	} else {
		if op.Command == "" {
			return fmt.Errorf("systemd_timer: missing command")
		}
		if op.OnCalendar == "" {
			return fmt.Errorf("systemd_timer: missing on_calendar")
		}
		opts = append(opts, opt.WithCommand(op.Command), opt.WithOnCalendar(op.OnCalendar))
		if op.OnBootSec != "" {
			opts = append(opts, opt.WithOnBootSec(op.OnBootSec))
		}
		if op.Persistent {
			opts = append(opts, opt.WithPersistent)
		}
		if op.Description != "" {
			opts = append(opts, opt.WithDescription(op.Description))
		}
		if op.ServiceDescription != "" {
			opts = append(opts, opt.WithServiceDescription(op.ServiceDescription))
		}
		if len(op.After) > 0 {
			opts = append(opts, opt.WithAfter(op.After...))
		}
		if len(op.Wants) > 0 {
			opts = append(opts, opt.WithWants(op.Wants...))
		}
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
