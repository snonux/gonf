package resource

import (
	"maps"
	"slices"
)

// Clone returns a deep copy of d: every slice, map and Payload gets its own
// backing storage, so mutating the copy never changes d and vice versa.
// What remains flat and reference-bearing here is only the cross-kind
// common core (Env, Watch, Deps); every kind-exclusive reference field
// (the former Args, Unless/OnlyIf, line/config-member/validator lists,
// TemplateData, ...) moved into a migrated kind's own Payload (task w62
// Layer 1), whose own Clone method — e.g. cmd.Payload's or
// configset.SetPayload's — deep-copies whatever it holds, including its own
// guard pointers or member/validator slices. Nil stays nil and empty stays
// empty (slices.Clone/maps.Clone/DraftPayload.Clone), so a clone lowers to
// the byte-identical plan op.
//
// This is the draft half of the copy contract (the WithEnv setters of task
// 872 are the option half, plan.Handler.ToOp the op half): RecordPlanDraft
// and AmendRegistered store one clone and hand their sink another, and
// RegisteredPlanDrafts returns clones, so the caller, the draft store, the
// recorder and every snapshot each own their draft.
func (d PlanDraft) Clone() PlanDraft {
	c := d
	c.Env = maps.Clone(d.Env)
	if d.Payload != nil {
		c.Payload = d.Payload.Clone()
	}
	c.Watch = slices.Clone(d.Watch)
	c.Deps = slices.Clone(d.Deps)
	return c
}
