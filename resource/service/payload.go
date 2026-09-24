package service

import "github.com/snonux/gonf/resource"

// Payload is the service kind's exclusive plan-draft fields (task w62
// Layer 1: splitting resource.PlanDraft's god struct into per-kind
// payloads). Restart/User/Watch/IfChanged stay flat because other kinds
// (systemd_timer, timer, daemon_reload, command) genuinely share their
// meaning. (*Service).planDraft sets PlanDraft.Payload to this type; the
// service plan.Handler's ToOp type-asserts it back.
type Payload struct {
	// Reload reloads a running service once, with no restart fallback.
	Reload bool
	// Flags are the WithFlags startup flags; HasFlags says they are managed
	// at all, so empty flags differ from unmanaged ones.
	Flags    string
	HasFlags bool
}

// Clone returns p unchanged: it has no reference fields to deep-copy.
// Implements resource.DraftPayload.
func (p Payload) Clone() resource.DraftPayload { return p }
