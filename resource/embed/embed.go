// Package embed holds state common to all concrete resource types (dependency
// tracking, absence marking, and change gating), embedded rather than
// redeclared.
package embed

import (
	"sort"

	"github.com/snonux/gonf/internal/logger"
)

// DependsOn is embedded into concrete resource types to give them the ability
// to accumulate dependency IDs supplied via the DependsOn option.
type DependsOn struct {
	// IDs lists the resource IDs this resource depends on; Present forwards
	// them into resource.Register so apply orders them first.
	IDs []string
}

// AddDependency records a single resource ID this resource depends on. It uses
// a pointer receiver so the mutation is visible to the embedding value.
func (d *DependsOn) AddDependency(id string) {
	d.IDs = append(d.IDs, id)
}

// SortedIDs returns the dependency IDs de-duplicated and sorted ascending, or
// nil when there are none. Plan draft builders use it to record a stable,
// deterministic dep list on the wire (plan.Op.Deps); the nil return keeps the
// field omitted for dep-free resources.
func (d *DependsOn) SortedIDs() []string {
	if len(d.IDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(d.IDs))
	ids := make([]string, 0, len(d.IDs))
	for _, id := range d.IDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Absence is embedded into concrete resource types that can be marked for
// removal via the IsAbsent option. It promotes an Absent field and a
// SetAbsent method to the embedding type.
type Absence struct {
	// Absent is set by the IsAbsent option to mark the resource for removal.
	Absent bool
}

// SetAbsent implements opt.Absentable, marking the resource for removal. It
// uses a pointer receiver so the mutation is visible to the embedding value.
func (a *Absence) SetAbsent() { a.Absent = true }

// ChangeGate is embedded into concrete resource types whose mutating action
// can be gated on watched resources' change reports (the OnChange option).
// The gate arms at registration time and is consulted at apply time (Holds):
// a gated action runs only when one of the watched resources reported a
// change (or would change, under dry-run) during the current apply.
//
// Besides the state, the embed owns the gate's behaviour (arming, the hold
// predicate, the held-action log line, and the plan-draft wiring) so every
// gated resource shares one copy. It stays a leaf package: the change oracle (resource.AnyChanged) is
// passed in and draft values are returned rather than written into a
// resource.PlanDraft, so embed never imports resource (only the leaf
// internal/logger, for LogHeld) and resource may use embed without an import
// cycle.
type ChangeGate struct {
	// Gated arms the change gate. Unarmed (false) means the resource's
	// mutating action runs unconditionally.
	Gated bool
	// Watch lists the resource IDs whose change reports fire the gated
	// action. The OnChange option fills it; the plan wire carries it on the
	// op's watch field so destination apply rebuilds the same gate.
	Watch []string
}

// SetChangeWatch arms the change gate and records ids as the watched
// resources. Multiple calls accumulate; it uses a pointer receiver so the
// mutation is visible to the embedding value. It implements
// opt.ChangeWatchable.
func (c *ChangeGate) SetChangeWatch(ids []string) {
	c.Gated = true
	c.Watch = append(c.Watch, ids...)
}

// Arm arms the gate without adding watched ids. Daemon-reload's own
// SetIfChanged (the legacy IfChanged option, opt.ChangeGated) delegates to
// it; its watch list then comes from WithWatch or, failing that, from the
// DependsOn ids. The embed deliberately does not provide SetIfChanged
// itself: that would make every embedder satisfy opt.ChangeGated, so an
// IfChanged option passed through the type-erased opt.Option path would
// silently arm a Service, Timer or Command (with nothing to watch, holding
// its action forever) instead of being rejected as unsupported.
func (c *ChangeGate) Arm() { c.Gated = true }

// LogHeld logs, at debug level, that the gate held the named action of the
// resource id this apply. It is the single copy of the operator-visible
// wording shared by every resource whose gated action is part of a larger
// convergence (Service's restart/reload, Timer's restart).
func (c *ChangeGate) LogHeld(id, action string) {
	logger.Debug("%s: %s held by change gate (no watched dependency changed)", id, action)
}

// ChangeOracle reports whether any of ids changed (or would change, under
// dry-run) during the current apply. resource.AnyChanged is the production
// oracle; tests may pass a stub.
type ChangeOracle func(ids ...string) bool

// Holds reports whether the gate suppresses the gated action this apply:
// it is armed and none of the watched ids changed according to changed. An
// armed gate whose watched ids never reported (unknown ids) holds too.
func (c *ChangeGate) Holds(changed ChangeOracle) bool {
	return c.HoldsWatching(changed, c.Watch)
}

// HoldsWatching is Holds for a resource that derives its effective watch
// list itself (daemon-reload merges legacy WithWatch ids and falls back to
// its DependsOn ids) instead of watching exactly Watch.
func (c *ChangeGate) HoldsWatching(changed ChangeOracle, watch []string) bool {
	return c.Gated && !changed(watch...)
}

// DraftGate returns the plan-draft change-gate fields (PlanDraft.IfChanged
// and PlanDraft.Watch): whether the gate is armed, and a copy of the watched
// ids when it is (nil otherwise, so the wire field stays omitted). The copy
// keeps a recorded draft from aliasing the resource's slice.
func (c *ChangeGate) DraftGate() (ifChanged bool, watch []string) {
	if !c.Gated {
		return false, nil
	}
	return true, append([]string(nil), c.Watch...)
}
