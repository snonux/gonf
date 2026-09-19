package user

import (
	"fmt"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler owns the user kind's record-time and apply-time wire form.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindUser, planHandler{})
}

// ToOp lowers a "user" resource draft to a plan.Op.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op:                  plan.KindUser,
		ID:                  d.ID,
		Name:                d.Name,
		PrimaryGroup:        d.PrimaryGroup,
		SupplementaryGroups: append([]string(nil), d.SupplementaryGroups...),
		Home:                d.Home,
		CreateHome:          d.CreateHome,
		Shell:               d.Shell,
		LoginClass:          d.LoginClass,
		System:              d.System,
		Deps:                d.Deps,
	}, nil
}

// Apply ensures the recorded account exists without destructive account
// changes, mirroring the direct resource path.
func (planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("user: missing name")
	}
	if op.Absent {
		return fmt.Errorf("user %q: absence is not supported", op.Name)
	}
	var opts []opt.LocalUserOption
	if op.PrimaryGroup != "" {
		opts = append(opts, opt.WithGroup(op.PrimaryGroup))
	}
	if len(op.SupplementaryGroups) > 0 {
		opts = append(opts, opt.WithSupplementaryGroups(op.SupplementaryGroups...))
	}
	if op.Home != "" {
		opts = append(opts, opt.WithHome(op.Home))
	}
	if op.CreateHome {
		opts = append(opts, opt.WithCreateHome)
	}
	if op.Shell != "" {
		opts = append(opts, opt.WithShell(op.Shell))
	}
	if op.LoginClass != "" {
		opts = append(opts, opt.WithClass(op.LoginClass))
	}
	if op.System {
		opts = append(opts, opt.WithSystem)
	}
	return Ensure(op.Name, opts...)
}
