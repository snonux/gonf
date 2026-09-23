package cmd

import (
	"slices"

	"github.com/snonux/gonf/resource"
)

// Payload is the command kind's exclusive plan-draft fields (task w62
// Layer 1: splitting resource.PlanDraft's god struct into per-kind
// payloads). Env stays a flat resource.PlanDraft field because package
// drafts reuse it too. (*Cmd).planDraft sets PlanDraft.Payload to this
// type; the command plan.Handler's ToOp type-asserts it back.
type Payload struct {
	// Bin is the executable to run.
	Bin string
	// Args is argv after Bin.
	Args []string
	// Dir is the working directory.
	Dir string
	// Creates skips the command when this path already exists.
	Creates string
	// Unless skips the command when the guard probe succeeds.
	Unless *resource.PlanGuardDraft
	// OnlyIf runs the command only when the guard probe succeeds.
	OnlyIf *resource.PlanGuardDraft
}

// Clone returns a deep copy of p: Args gets its own backing array, and
// Unless/OnlyIf get their own pointee (nil stays nil). Implements
// resource.DraftPayload.
func (p Payload) Clone() resource.DraftPayload {
	c := p
	c.Args = slices.Clone(p.Args)
	c.Unless = cloneGuard(p.Unless)
	c.OnlyIf = cloneGuard(p.OnlyIf)
	return c
}

// cloneGuard deep-copies a guard probe, including its Args and ExpectExit
// (nil stays nil). Mirrors resource.PlanDraft's own cloneGuard, kept here
// too since resource/draft_clone.go cannot reach into this package's
// payload to clone it generically.
func cloneGuard(g *resource.PlanGuardDraft) *resource.PlanGuardDraft {
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
