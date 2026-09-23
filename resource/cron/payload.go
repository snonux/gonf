package cron

import (
	"slices"

	"github.com/snonux/gonf/resource"
)

// Payload is the cron kind's exclusive plan-draft fields (task w62 Layer 1:
// splitting resource.PlanDraft's god struct into per-kind payloads). It
// holds every "cron" draft field except the ones cron genuinely shares with
// another kind (Command, reused by systemd_timer for its own ExecStart
// line, stays on resource.PlanDraft until systemd_timer migrates too) and
// the cross-kind common core (ID, Name, Absent, Deps, Sensitive, ...),
// which also stays flat. (*Cron).planDraft sets PlanDraft.Payload to this
// type instead of the fields it replaces; the cron plan.Handler's ToOp type
// -asserts it back.
type Payload struct {
	// CronUser is the crontab owner (default root).
	CronUser string
	// LegacyCommand opts into removing one exact unmanaged command.
	LegacyCommand string
	// Schedule holds the five space-separated cron time fields
	// (minute hour monthday month weekday).
	Schedule string
	// CronEnv lists KEY=VAL environment lines above the cron job.
	CronEnv []string
}

// Clone returns a deep copy of p, giving CronEnv its own backing array.
// Implements resource.DraftPayload.
func (p Payload) Clone() resource.DraftPayload {
	c := p
	c.CronEnv = slices.Clone(p.CronEnv)
	return c
}
