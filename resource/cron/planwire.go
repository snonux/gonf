package cron

import (
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler is the cron kind's plan.Handler: see resource/pkg/planwire.go
// for why record-time ToOp and apply-time Apply live together in the
// resource package instead of api/packager.go's draftToOp and plan/apply.go's
// applyCron.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindCron, planHandler{})
}

// ToOp lowers a "cron" resource draft to a plan.Op. The cron-exclusive
// fields come from d.Payload (resource/cron.Payload, task w62 Layer 1); a
// "cron" draft without one is a record-time bug (planDraft always sets it),
// reported like any other handler error rather than panicking, since ToOp
// runs at record time on recipe-shaped input, not on a programmer-only path.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(Payload)
	if !ok {
		return plan.Op{}, fmt.Errorf("cron: draft missing cron.Payload (got %T)", d.Payload)
	}
	return plan.Op{
		Op:      plan.KindCron,
		ID:      d.ID,
		Name:    d.Name,
		Absent:  d.Absent,
		Command: d.Command,
		Deps:    slices.Clone(d.Deps),
		Payload: plan.CronPayload{
			CronUser:      p.CronUser,
			LegacyCommand: p.LegacyCommand,
			Schedule:      p.Schedule,
			CronEnv:       slices.Clone(p.CronEnv),
		},
	}, nil
}

// Apply installs or removes the named crontab entry, mirroring
// resource/cron's own Present/Absent option handling.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("cron: missing name")
	}
	// A comma-ok assertion, not a "missing payload" error: unlike ToOp (fed
	// only trusted draft data planDraft() always populates), Apply may see
	// an op decoded from an arbitrary plan.jsonl. A nil or mistyped Payload
	// degrades to the zero CronPayload — every cron-exclusive field reads
	// as unset, which the schedule check below already turns into a clean
	// error for a present job, and is simply inert for an absent one.
	p, _ := op.Payload.(plan.CronPayload)
	var opts []opt.CronOption
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if p.CronUser != "" {
		opts = append(opts, opt.WithCronUser(p.CronUser))
	}
	if op.Command != "" {
		opts = append(opts, opt.WithCommand(op.Command))
	}
	if p.LegacyCommand != "" {
		opts = append(opts, opt.WithLegacyCommand(p.LegacyCommand))
	}
	// A present cron job needs a schedule: silently falling back to the
	// resource default (* * * * *, every minute) would run the command far
	// more often than the plan author intended.
	fields := strings.Fields(p.Schedule)
	if !op.Absent && len(fields) != 5 {
		return fmt.Errorf("cron: schedule %q must contain 5 whitespace-separated fields", p.Schedule)
	}
	if len(fields) == 5 {
		opts = append(opts,
			opt.WithMinute(fields[0]),
			opt.WithHour(fields[1]),
			opt.WithMonthday(fields[2]),
			opt.WithMonth(fields[3]),
			opt.WithWeekday(fields[4]),
		)
	}
	for _, kv := range p.CronEnv {
		opts = append(opts, opt.WithCronEnv(kv))
	}
	// A sensitive op (scan-detected or WithSensitive at record time)
	// rebuilds a sensitive Cron, as every accepting kind's handler does.
	if op.Sensitive {
		opts = append(opts, opt.WithSensitive)
	}
	return Ensure(op.Name, opts...)
}
