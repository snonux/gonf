package link

import "github.com/snonux/gonf/resource"

// Payload is the "link" kind's exclusive plan-draft fields (task w62
// Layer 1: splitting resource.PlanDraft's god struct into per-kind
// payloads). (*Link).planDraft sets PlanDraft.Payload to this type; the
// link plan.Handler's ToOp type-asserts it back. IfExistsPayload is the
// separate payload for the "link_if_exists" kind this package also
// handles — a different kind gets its own type rather than reusing this
// one with unused fields.
type Payload struct {
	// Symlink is the symlink target.
	Symlink string
	// Hardlink is the hardlink target, set instead of Symlink.
	Hardlink string
}

// Clone returns p unchanged: it has no reference fields to deep-copy.
// Implements resource.DraftPayload.
func (p Payload) Clone() resource.DraftPayload { return p }

// IfExistsPayload is the "link_if_exists" kind's one exclusive plan-draft
// field. api.LinkIfExists (in plan-record mode; SymlinkMap calls it per
// pair) sets PlanDraft.Payload to this type directly — this kind has no
// standalone resource.Present path (see AGENTS.md, "Resource Management")
// — and the link_if_exists plan.Handler's ToOp type-asserts it back.
type IfExistsPayload struct {
	// Target is the existence-checked path.
	Target string
}

// Clone returns p unchanged: it has no reference fields to deep-copy.
// Implements resource.DraftPayload.
func (p IfExistsPayload) Clone() resource.DraftPayload { return p }
