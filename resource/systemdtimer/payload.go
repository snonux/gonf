package systemdtimer

import (
	"slices"

	"github.com/snonux/gonf/resource"
)

// Payload is the systemd_timer kind's exclusive plan-draft fields (task w62
// Layer 1: splitting resource.PlanDraft's god struct into per-kind
// payloads). Command stays a flat resource.PlanDraft field because cron
// reuses it too; User/Restart/EnableOnly/Absent/Name/ID/Deps stay flat as
// the cross-kind common core. (*SystemdTimer).planDraft sets
// PlanDraft.Payload to this type; the systemd_timer plan.Handler's ToOp
// type-asserts it back.
type Payload struct {
	// OnCalendar is the systemd OnCalendar= expression.
	OnCalendar string
	// OnBootSec is the systemd OnBootSec= delay.
	OnBootSec string
	// Persistent sets Persistent=true on the unit.
	Persistent bool
	// Description is the [Unit] Description.
	Description string
	// ServiceDescription is the companion oneshot .service Description.
	ServiceDescription string
	// After lists After= dependencies on the companion oneshot .service.
	After []string
	// Wants lists Wants= dependencies on the companion oneshot .service.
	Wants []string
}

// Clone returns a deep copy of p, giving After and Wants their own backing
// arrays. Implements resource.DraftPayload.
func (p Payload) Clone() resource.DraftPayload {
	c := p
	c.After = slices.Clone(p.After)
	c.Wants = slices.Clone(p.Wants)
	return c
}
