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

// ToOp lowers a "systemd_timer" resource draft to a plan.Op. The
// exclusive fields come from d.Payload (Payload, task w62 Layer 1); a
// "systemd_timer" draft without one is a record-time bug (planDraft always
// sets it), reported like any other handler error rather than panicking.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(Payload)
	if !ok {
		return plan.Op{}, fmt.Errorf("systemd_timer: draft missing systemdtimer.Payload (got %T)", d.Payload)
	}
	return plan.Op{
		Op:         plan.KindSystemdTimer,
		ID:         d.ID,
		Name:       d.Name,
		Absent:     d.Absent,
		User:       d.User,
		Restart:    d.Restart,
		EnableOnly: d.EnableOnly,
		Command:    d.Command,
		Deps:       slices.Clone(d.Deps),
		Payload: plan.SystemdTimerPayload{
			OnCalendar:         p.OnCalendar,
			OnBootSec:          p.OnBootSec,
			Persistent:         p.Persistent,
			Description:        p.Description,
			ServiceDescription: p.ServiceDescription,
			After:              slices.Clone(p.After),
			Wants:              slices.Clone(p.Wants),
		},
	}, nil
}

// Apply installs/enables/starts, restarts, or stops/disables/removes the
// declarative timer + oneshot service pair, mirroring the resource's own
// option handling exactly.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("systemd_timer: missing name")
	}
	// presentOptions' own plan.PayloadOf read (see its doc comment) degrades
	// cleanly to its "missing on_calendar" refusal for a decoded or
	// mistyped Payload.
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
	// p reads as the zero SystemdTimerPayload for a decoded or mistyped
	// Payload (see plan.PayloadOf's doc comment).
	p := plan.PayloadOf[plan.SystemdTimerPayload](op)
	if p.OnCalendar == "" {
		return nil, fmt.Errorf("systemd_timer: missing on_calendar")
	}
	opts := []opt.SystemdTimerOption{opt.WithCommand(op.Command), opt.WithOnCalendar(p.OnCalendar)}
	if p.OnBootSec != "" {
		opts = append(opts, opt.WithOnBootSec(p.OnBootSec))
	}
	if p.Persistent {
		opts = append(opts, opt.WithPersistent)
	}
	if p.Description != "" {
		opts = append(opts, opt.WithDescription(p.Description))
	}
	if p.ServiceDescription != "" {
		opts = append(opts, opt.WithServiceDescription(p.ServiceDescription))
	}
	if len(p.After) > 0 {
		opts = append(opts, opt.WithAfter(p.After...))
	}
	if len(p.Wants) > 0 {
		opts = append(opts, opt.WithWants(p.Wants...))
	}
	return opts, nil
}
