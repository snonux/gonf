package cron

import (
	"fmt"
	"strings"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// planHandler is the cron kind's plan.Handler: see resource/pkg/planwire.go
// for why record-time ToOp and apply-time Apply live together in the
// resource package instead of api/plan.go's draftToOp and plan/apply.go's
// applyCron.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindCron, planHandler{})
}

// ToOp lowers a "cron" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:       plan.KindCron,
		ID:       d.ID,
		Name:     d.Name,
		Absent:   d.Absent,
		CronUser: d.CronUser,
		Command:  d.Command,
		Schedule: d.Schedule,
		CronEnv:  d.CronEnv,
		Deps:     d.Deps,
	}, nil
}

// Apply installs or removes the named crontab entry, mirroring
// resource/cron's own Present/Absent option handling.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("cron: missing name")
	}
	var opts []opt.Option
	if op.Absent {
		opts = append(opts, opt.IsAbsent)
	}
	if op.CronUser != "" {
		opts = append(opts, opt.WithCronUser(op.CronUser))
	}
	if op.Command != "" {
		opts = append(opts, opt.WithCommand(op.Command))
	}
	// A present cron job needs a schedule: silently falling back to the
	// resource default (* * * * *, every minute) would run the command far
	// more often than the plan author intended.
	fields := strings.Fields(op.Schedule)
	if !op.Absent && len(fields) != 5 {
		return fmt.Errorf("cron: schedule %q must contain 5 whitespace-separated fields", op.Schedule)
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
	for _, kv := range op.CronEnv {
		opts = append(opts, opt.WithCronEnv(kv))
	}
	return Ensure(op.Name, opts...)
}
