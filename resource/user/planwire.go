package user

import (
	"fmt"

	internaluser "github.com/snonux/gonf/internal/user"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler owns the user kind's record-time and apply-time wire form.
type planHandler struct{}

func init() {
	plan.RegisterHandler(plan.KindUser, planHandler{})
}

// ToOp lowers a "user" resource draft to a plan.Op. An opted-in managed home
// is validated here so a malformed recipe fails at `gonf plan` time on the
// controller instead of on the destination. Other fields keep their existing
// apply-time validation so recipes that do not opt in record exactly as before.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	if d.ManageHome {
		if err := internaluser.ValidateManagedHome(d.Name, d.Home); err != nil {
			return plan.Op{}, err
		}
	}
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
		ManageHome:          d.ManageHome,
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
	return Ensure(op.Name, opOptions(op)...)
}

// opOptions rebuilds the recipe options from a recorded op, so destination
// apply goes through exactly the same resource path as a direct apply.
func opOptions(op plan.Op) []opt.LocalUserOption {
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
	if op.ManageHome {
		opts = append(opts, opt.WithManageHome)
	}
	return opts
}
