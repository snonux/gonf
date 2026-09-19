package cmd

import (
	"fmt"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler is the command kind's plan.Handler: see
// resource/pkg/planwire.go for why record-time ToOp and apply-time Apply
// live together in the resource package instead of api/plan.go's
// draftToOp and plan/apply.go's applyCommand.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindCommand, planHandler{})
}

// ToOp lowers a "command" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:      plan.KindCommand,
		ID:      d.ID,
		Name:    d.Name,
		Bin:     d.Bin,
		Args:    d.Args,
		Dir:     d.Dir,
		Env:     d.Env,
		Creates: d.Creates,
		Unless:  planGuard(d.Unless),
		OnlyIf:  planGuard(d.OnlyIf),
		Deps:    d.Deps,
	}, nil
}

// Apply runs the command (subject to Creates/Unless/OnlyIf guards),
// mirroring the resource's own guard handling exactly.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Bin == "" {
		return fmt.Errorf("command: missing bin")
	}
	var opts []opt.CommandOption
	if op.Name != "" {
		opts = append(opts, opt.WithName(op.Name))
	}
	if op.Dir != "" {
		dirPath, err := plan.ExpandPath(op.Dir)
		if err != nil {
			return err
		}
		opts = append(opts, opt.WithDir(dirPath))
	}
	if op.Creates != "" {
		creates, err := plan.ExpandPath(op.Creates)
		if err != nil {
			return err
		}
		opts = append(opts, opt.Creates(creates))
	}
	if len(op.Env) > 0 {
		opts = append(opts, opt.WithEnv(op.Env))
	}
	if op.Unless != nil {
		opts = append(opts, plan.GuardOptions(op.Unless, true)...)
	}
	if op.OnlyIf != nil {
		opts = append(opts, plan.GuardOptions(op.OnlyIf, false)...)
	}
	return Ensure(op.Bin, append([]string(nil), op.Args...), opts...)
}

// planGuard converts a package-neutral guard draft to the plan wire Guard,
// mirroring the api/plan.go draftGuard helper this replaces.
func planGuard(g *resource.PlanGuardDraft) *plan.Guard {
	if g == nil {
		return nil
	}
	return &plan.Guard{
		Bin:          g.Bin,
		Args:         g.Args,
		ExpectStdout: g.ExpectStdout,
		ExpectExit:   g.ExpectExit,
	}
}
