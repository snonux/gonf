package user

import (
	"fmt"
	"slices"

	internaluser "github.com/snonux/gonf/internal/user"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// planHandler owns the user kind's record-time and apply-time wire form.
// backend, when set, replaces the running host's backend for Apply; the
// registered handler leaves it nil. Tests set it instead of mutating
// package state.
type planHandler struct {
	backend internaluser.Backend
}

func init() {
	plan.RegisterHandler(plan.KindUser, planHandler{})
}

// ToOp lowers a "user" resource draft to a plan.Op and rejects, at `gonf
// plan` time on the controller, every request that could only fail on the
// destination. The target platform is unknown while recording, so only
// requests every backend refuses are rejected (see
// internaluser.ValidateForAnyBackend): malformed names, NUL bytes, and
// combinations no platform's declared capabilities accept. An opted-in
// managed home is checked first so its record-time error text stays the one
// ValidateManagedHome has always reported. Any request some platform accepts
// records exactly as before and is checked again on the destination.
func (planHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	p, ok := d.Payload.(Payload)
	if !ok {
		return plan.Op{}, fmt.Errorf("user: draft missing user.Payload (got %T)", d.Payload)
	}
	if p.ManageHome {
		if err := internaluser.ValidateManagedHome(d.Name, p.Home); err != nil {
			return plan.Op{}, err
		}
	}
	op := draftOp(d, p)
	if err := internaluser.ValidateForAnyBackend(newUser(op.Name, opOptions(op)).desired()); err != nil {
		return plan.Op{}, err
	}
	return op, nil
}

// draftOp copies a user draft, and its Payload p, into its wire form.
func draftOp(d resource.PlanDraft, p Payload) plan.Op {
	return plan.Op{
		Op:   plan.KindUser,
		ID:   d.ID,
		Name: d.Name,
		Deps: slices.Clone(d.Deps),
		Payload: plan.UserPayload{
			PrimaryGroup:        p.PrimaryGroup,
			SupplementaryGroups: slices.Clone(p.SupplementaryGroups),
			Home:                p.Home,
			CreateHome:          p.CreateHome,
			Shell:               p.Shell,
			LoginClass:          p.LoginClass,
			System:              p.System,
			ManageHome:          p.ManageHome,
		},
	}
}

// Apply ensures the recorded account exists without destructive account
// changes, mirroring the direct resource path.
func (h planHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" {
		return fmt.Errorf("user: missing name")
	}
	if op.Absent {
		return fmt.Errorf("user %q: absence is not supported", op.Name)
	}
	return h.newUser(op).apply()
}

// newUser rebuilds the recorded user through the same constructor as a
// direct apply, with the handler's backend when one is injected.
func (h planHandler) newUser(op plan.Op) *User {
	if h.backend == nil {
		return newUser(op.Name, opOptions(op))
	}
	return newUserWith(h.backend, op.Name, opOptions(op))
}

// opOptions rebuilds the recipe options from a recorded op, so destination
// apply goes through exactly the same resource path as a direct apply. p
// reads as the zero UserPayload for a decoded or mistyped Payload (see
// plan.PayloadOf's doc comment) — every user-exclusive field reads as
// unset, which simply yields a bare account request (name only), same as a
// recipe that set no options at all.
func opOptions(op plan.Op) []opt.LocalUserOption {
	p := plan.PayloadOf[plan.UserPayload](op)
	var opts []opt.LocalUserOption
	if p.PrimaryGroup != "" {
		opts = append(opts, opt.WithGroup(p.PrimaryGroup))
	}
	if len(p.SupplementaryGroups) > 0 {
		opts = append(opts, opt.WithSupplementaryGroups(p.SupplementaryGroups...))
	}
	if p.Home != "" {
		opts = append(opts, opt.WithHome(p.Home))
	}
	if p.CreateHome {
		opts = append(opts, opt.WithCreateHome)
	}
	if p.Shell != "" {
		opts = append(opts, opt.WithShell(p.Shell))
	}
	if p.LoginClass != "" {
		opts = append(opts, opt.WithClass(p.LoginClass))
	}
	if p.System {
		opts = append(opts, opt.WithSystem)
	}
	if p.ManageHome {
		opts = append(opts, opt.WithManageHome)
	}
	return opts
}
