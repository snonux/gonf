package configset

import (
	"github.com/snonux/gonf/resource"
)

// SetPayload is the "config_set" kind's exclusive plan-draft fields (task
// w62 Layer 1: splitting resource.PlanDraft's god struct into per-kind
// payloads). Name/ID/Deps/Sensitive stay flat since they are the
// cross-kind common core. (*spec).planDraft sets PlanDraft.Payload to this
// type; the config_set plan.Handler's ToOp type-asserts it back.
type SetPayload struct {
	// ConfigMembers are the set's member files, in declaration order.
	ConfigMembers []resource.PlanConfigMember
	// Validators are the argv commands run against the complete staged set.
	Validators []resource.PlanArgv
	// Chroot is the optional chroot directory every member and the staging
	// directory must live under.
	Chroot string
	// StagingDir is the directory that receives the private staging
	// directory. Empty means the members' deepest common directory.
	StagingDir string
}

// Clone returns a deep copy of p: ConfigMembers' and Validators' own
// reference fields (Content, Args) get their own backing storage, via
// resource.ClonePlanConfigMembers/ClonePlanArgvs (task 2e2 exported these
// next to resource.PlanConfigMember/PlanArgv, replacing this package's own
// former private copies). Implements resource.DraftPayload.
func (p SetPayload) Clone() resource.DraftPayload {
	c := p
	c.ConfigMembers = resource.ClonePlanConfigMembers(p.ConfigMembers)
	c.Validators = resource.ClonePlanArgvs(p.Validators)
	return c
}

// MemberPayload is the "config_set_member" kind's one exclusive plan-draft
// field. memberDraft sets PlanDraft.Payload to this type; the
// config_set_member plan.Handler's ToOp type-asserts it back.
type MemberPayload struct {
	// Member is the member key within the owning set (Name carries the
	// set's own name).
	Member string
}

// Clone returns p unchanged: it has no reference fields to deep-copy.
// Implements resource.DraftPayload.
func (p MemberPayload) Clone() resource.DraftPayload { return p }
