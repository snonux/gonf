package cmd

import (
	"fmt"
	"maps"
	"slices"

	"github.com/snonux/gonf/internal/runners"
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

// ToOp lowers a "command" resource draft to a plan.Op. The command-exclusive
// fields come from d.Payload (resource/cmd.Payload, task w62 Layer 1),
// carried into the op's own plan.CommandPayload (task 7e2, Layer 2's third
// slice) rather than staying flat on plan.Op; a "command" draft without a
// resource/cmd.Payload is a record-time bug (planDraft always sets it),
// reported like any other handler error rather than panicking.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(Payload)
	if !ok {
		return plan.Op{}, fmt.Errorf("command: draft missing cmd.Payload (got %T)", d.Payload)
	}
	op := plan.Op{
		Op:   plan.KindCommand,
		ID:   d.ID,
		Name: d.Name,
		Env:  maps.Clone(d.Env),
		Deps: slices.Clone(d.Deps),
		Payload: plan.CommandPayload{
			Bin:     p.Bin,
			Args:    slices.Clone(p.Args),
			Dir:     p.Dir,
			Creates: p.Creates,
			Unless:  planGuard(p.Unless),
			OnlyIf:  planGuard(p.OnlyIf),
		},
	}
	// Change gate (schema v11): OnChange arms IfChanged with the watched ids.
	if d.IfChanged {
		op.IfChanged = true
		op.Watch = slices.Clone(d.Watch)
	}
	return op, nil
}

// Apply runs the command (subject to Creates/Unless/OnlyIf guards and the
// OnChange change gate), mirroring the resource's own guard handling
// exactly. ctx.Runners.Command, when this apply had one injected (task qb2;
// nil in every real apply), replaces the real internal/exec runner.
func (planHandler) Apply(op plan.Op, ctx plan.ApplyContext) error {
	// p reads as the zero CommandPayload for a decoded or mistyped Payload
	// (see plan.PayloadOf's doc comment), which the missing-bin check below
	// already turns into a clean error.
	p := plan.PayloadOf[plan.CommandPayload](op)
	if p.Bin == "" {
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
	if p.Dir != "" {
		dirPath, err := plan.ExpandPath(p.Dir)
		if err != nil {
			return err
		}
		opts = append(opts, opt.WithDir(dirPath))
	}
	if p.Creates != "" {
		creates, err := plan.ExpandPath(p.Creates)
		if err != nil {
			return err
		}
		opts = append(opts, opt.Creates(creates))
	}
	if len(op.Env) > 0 {
		opts = append(opts, opt.WithEnv(op.Env))
	}
	if p.Unless != nil {
		opts = append(opts, plan.GuardOptions(p.Unless, true)...)
	}
	if p.OnlyIf != nil {
		opts = append(opts, plan.GuardOptions(p.OnlyIf, false)...)
	}
	// A sensitive op (scan-detected or WithSensitive at record time)
	// rebuilds a sensitive command, which withholds argv and output.
	if op.Sensitive {
		opts = append(opts, opt.WithSensitive)
	}
	return ensureWith(runners.CommandOf(ctx.Runners), p.Bin, append([]string(nil), p.Args...), opts)
}

// planGuard converts a package-neutral guard draft to the plan wire Guard,
// mirroring the api/plan.go draftGuard helper this replaces.
func planGuard(g *resource.PlanGuardDraft) *plan.Guard {
	if g == nil {
		return nil
	}
	guard := &plan.Guard{
		Bin:          g.Bin,
		Args:         slices.Clone(g.Args),
		ExpectStdout: g.ExpectStdout,
	}
	if g.ExpectExit != nil {
		exit := *g.ExpectExit
		guard.ExpectExit = &exit
	}
	return guard
}
