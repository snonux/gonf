package configset

import (
	"slices"

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
// reference fields (Content, Args) get their own backing storage.
// Implements resource.DraftPayload.
func (p SetPayload) Clone() resource.DraftPayload {
	c := p
	c.ConfigMembers = cloneConfigMembers(p.ConfigMembers)
	c.Validators = cloneArgvs(p.Validators)
	return c
}

// cloneConfigMembers deep-copies config-set members, including each
// member's Content bytes (nil stays nil, empty stays empty). Mirrors
// resource.PlanDraft.Clone's own former helper of the same shape, kept
// here too since resource/draft_clone.go cannot reach into this package's
// payload to clone it generically.
func cloneConfigMembers(members []resource.PlanConfigMember) []resource.PlanConfigMember {
	if members == nil {
		return nil
	}
	c := make([]resource.PlanConfigMember, len(members))
	for i, m := range members {
		m.Content = slices.Clone(m.Content)
		c[i] = m
	}
	return c
}

// cloneArgvs deep-copies argv commands, including each one's Args (nil
// stays nil, empty stays empty).
func cloneArgvs(argvs []resource.PlanArgv) []resource.PlanArgv {
	if argvs == nil {
		return nil
	}
	c := make([]resource.PlanArgv, len(argvs))
	for i, a := range argvs {
		a.Args = slices.Clone(a.Args)
		c[i] = a
	}
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
