package user

import (
	"slices"

	"github.com/snonux/gonf/resource"
)

// Payload is the user kind's exclusive plan-draft fields (task w62 Layer 1:
// splitting resource.PlanDraft's god struct into per-kind payloads). Every
// "user" draft field is exclusive to this kind, so Payload carries all of
// them; only the cross-kind common core (ID, Name, Deps, ...) stays on
// resource.PlanDraft. (*User).planDraft sets PlanDraft.Payload to this
// type; the user plan.Handler's ToOp/draftOp type-assert it back.
type Payload struct {
	// PrimaryGroup and SupplementaryGroups are the requested groups. Only
	// missing supplementary memberships are added; no existing membership
	// or primary group is removed or rewritten.
	PrimaryGroup        string
	SupplementaryGroups []string
	// Home, CreateHome, Shell, LoginClass, and System are creation-time
	// account attributes, retained on the wire so destination apply makes
	// the same decision for a missing account as a direct resource apply.
	// Home is also used for an existing account when ManageHome opts in.
	Home       string
	CreateHome bool
	Shell      string
	LoginClass string
	System     bool
	// ManageHome is the one opt-in exception to creation-only attributes:
	// it converges an existing account's passwd home field to Home,
	// without moving, creating, or chowning the directory.
	ManageHome bool
}

// Clone returns a deep copy of p, giving SupplementaryGroups its own
// backing array. Implements resource.DraftPayload.
func (p Payload) Clone() resource.DraftPayload {
	c := p
	c.SupplementaryGroups = slices.Clone(p.SupplementaryGroups)
	return c
}
