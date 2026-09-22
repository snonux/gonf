package resource

import (
	"maps"
	"slices"
)

// Clone returns a deep copy of d: every slice, map and pointer field
// (Args, Env, Deps, Watch, the line/group/cron/unit lists, ValidationArgs,
// the encoded TemplateData, the Unless/OnlyIf guards with their Args and
// ExpectExit, and the config-set members' Content and validators' Args)
// gets its own backing
// storage, so mutating the copy never changes d and vice versa. Nil stays
// nil and empty stays empty (slices.Clone/maps.Clone), so a clone lowers to
// the byte-identical plan op.
//
// This is the draft half of the copy contract (the WithEnv setters of task
// 872 are the option half, plan.Handler.ToOp the op half): RecordPlanDraft
// and AmendRegistered store one clone and hand their sink another, and
// RegisteredPlanDrafts returns clones, so the caller, the draft store, the
// recorder and every snapshot each own their draft. TemplateData is carried
// as its JSON encoding rather than the recipe's live value, so it is copied
// like any other slice. The only reference Clone shares is
// TemplateDataErr: an error is an immutable value, not storage anyone
// writes through.
func (d PlanDraft) Clone() PlanDraft {
	c := d
	c.TemplateData = slices.Clone(d.TemplateData)
	c.ValidationArgs = slices.Clone(d.ValidationArgs)
	c.SupplementaryGroups = slices.Clone(d.SupplementaryGroups)
	c.AddLines = slices.Clone(d.AddLines)
	c.RemoveLines = slices.Clone(d.RemoveLines)
	c.Args = slices.Clone(d.Args)
	c.Env = maps.Clone(d.Env)
	c.Unless = cloneGuard(d.Unless)
	c.OnlyIf = cloneGuard(d.OnlyIf)
	c.CronEnv = slices.Clone(d.CronEnv)
	c.After = slices.Clone(d.After)
	c.Wants = slices.Clone(d.Wants)
	c.Watch = slices.Clone(d.Watch)
	c.ConfigMembers = cloneConfigMembers(d.ConfigMembers)
	c.Validators = cloneArgvs(d.Validators)
	c.Deps = slices.Clone(d.Deps)
	return c
}

// cloneGuard deep-copies a guard probe, including its Args and ExpectExit
// (nil stays nil).
func cloneGuard(g *PlanGuardDraft) *PlanGuardDraft {
	if g == nil {
		return nil
	}
	c := *g
	c.Args = slices.Clone(g.Args)
	if g.ExpectExit != nil {
		exit := *g.ExpectExit
		c.ExpectExit = &exit
	}
	return &c
}

// cloneConfigMembers deep-copies config-set members, including each
// member's Content bytes (nil stays nil, empty stays empty).
func cloneConfigMembers(members []PlanConfigMember) []PlanConfigMember {
	if members == nil {
		return nil
	}
	c := make([]PlanConfigMember, len(members))
	for i, m := range members {
		m.Content = slices.Clone(m.Content)
		c[i] = m
	}
	return c
}

// cloneArgvs deep-copies argv commands, including each one's Args (nil
// stays nil, empty stays empty).
func cloneArgvs(argvs []PlanArgv) []PlanArgv {
	if argvs == nil {
		return nil
	}
	c := make([]PlanArgv, len(argvs))
	for i, a := range argvs {
		a.Args = slices.Clone(a.Args)
		c[i] = a
	}
	return c
}
