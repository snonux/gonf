package cmd

import (
	"fmt"
	"maps"

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
	op := plan.Op{
		Op:      plan.KindCommand,
		ID:      d.ID,
		Name:    d.Name,
		Bin:     d.Bin,
		Args:    d.Args,
		Dir:     d.Dir,
		Env:     maps.Clone(d.Env), // op must not alias the stored draft's map
		Creates: d.Creates,
		Unless:  planGuard(d.Unless),
		OnlyIf:  planGuard(d.OnlyIf),
		Deps:    d.Deps,
	}
	// Change gate (schema v11): OnChange arms IfChanged with the watched ids.
	if d.IfChanged {
		op.IfChanged = true
		op.Watch = append([]string(nil), d.Watch...)
	}
	return op, nil
}

// Apply runs the command (subject to Creates/Unless/OnlyIf guards and the
// OnChange change gate), mirroring the resource's own guard handling exactly.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Bin == "" {
		return fmt.Errorf("command: missing bin")
	}
	var opts []opt.CommandOption
	if op.Name != "" {
		opts = append(opts, opt.WithName(op.Name))
	}
	// The recorded gate is rebuilt by the rule every gated kind shares
	// (opt.RecordedChangeGate): a gated op without watch ids is an error.
	gate, err := opt.RecordedChangeGate(string(plan.KindCommand), op.IfChanged, op.Watch)
	if err != nil {
		return err
	}
	if gate != nil {
		opts = append(opts, gate)
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
