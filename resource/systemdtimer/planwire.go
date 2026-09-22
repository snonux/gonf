package systemdtimer

import (
	"fmt"
	"slices"

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
		After:              slices.Clone(d.After),
		Wants:              slices.Clone(d.Wants),
		Deps:               slices.Clone(d.Deps),
	}, nil
}

// Apply installs/enables/starts, restarts, or stops/disables/removes the
// declarative timer + oneshot service pair, mirroring the resource's own
// option handling exactly.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("systemd_timer: missing name")
	}
	opts := []opt.SystemdTimerOption{opt.IsAbsent}
	if !op.Absent {
		var err error
		if opts, err = presentOptions(op); err != nil {
			return err
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
	// A sensitive op (scan-detected or WithSensitive at record time)
	// rebuilds a sensitive timer, whose unit files are written as
	// sensitive files.
	if op.Sensitive {
		opts = append(opts, opt.WithSensitive)
	}
	return Ensure(op.Name, opts...)
}

// presentOptions translates a present systemd_timer op's unit fields into
// the options a direct recipe would pass, requiring the command and
// calendar a present timer needs.
func presentOptions(op plan.Op) ([]opt.SystemdTimerOption, error) {
	if op.Command == "" {
		return nil, fmt.Errorf("systemd_timer: missing command")
	}
	if op.OnCalendar == "" {
		return nil, fmt.Errorf("systemd_timer: missing on_calendar")
	}
	opts := []opt.SystemdTimerOption{opt.WithCommand(op.Command), opt.WithOnCalendar(op.OnCalendar)}
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
	return opts, nil
}
