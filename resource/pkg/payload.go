package pkg

import "github.com/snonux/gonf/resource"

// Payload is the package kind's one exclusive plan-draft field (task w62
// Layer 1: splitting resource.PlanDraft's god struct into per-kind
// payloads). (*Package).planDraft sets PlanDraft.Payload to this type; the
// package plan.Handler's ToOp type-asserts it back.
type Payload struct {
	// Latest marks a draft configured with IsLatest: destination apply must
	// run the backend's upgrade-check path (dnf update / pkg upgrade /
	// pkg_add -u / pkgin install) instead of a plain install.
	Latest bool
}

// Clone returns p unchanged: it has no reference fields to deep-copy.
// Implements resource.DraftPayload.
func (p Payload) Clone() resource.DraftPayload { return p }
