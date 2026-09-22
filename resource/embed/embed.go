// Package embed holds state common to all concrete resource types (dependency
// tracking, absence marking, and change gating), embedded rather than
// redeclared.
package embed

import (
	"errors"
	"slices"
	"sort"

	"github.com/snonux/gonf/internal/logger"
)

// errNothingToWatch is CheckWatch's error: an armed change gate with an empty
// watch list.
var errNothingToWatch = errors.New("change gate armed with nothing to watch, so it could never fire " +
	"(a DaemonReload armed by IfChanged with neither WithWatch ids nor DependsOn to fall back to); " +
	"use OnChange(resources...) or WatchChanges(ids...)")

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
// can be gated on watched resources' change reports. It holds the one
// watch list per resource: OnChange, WatchChanges and the legacy IfChanged
// arm it through SetChangeWatch (opt.ChangeWatchable), and daemon-reload
// folds its legacy WithWatch ids into it once its options ran. The gate
// arms at registration time and is consulted at apply time (Holds): a
// gated action runs only when one of the watched resources reported a
// change (or would change, under dry-run) during the current apply.
//
// Besides the state, the embed owns the gate's behaviour (arming with
// de-duplication, the nothing-to-watch check, the hold predicate, the
// held-action log line, and the plan-draft wiring) so every gated resource
// shares one copy. It stays a leaf package: the change oracle
// (resource.AnyChanged) is passed in and draft values are returned rather
// than written into a resource.PlanDraft, so embed never imports resource
// (only the leaf internal/logger, for LogHeld) and resource may use embed
// without an import cycle.
type ChangeGate struct {
	// Gated arms the change gate. Unarmed (false) means the resource's
	// mutating action runs unconditionally.
	Gated bool
	// Watch lists the resource IDs whose change reports fire the gated
	// action, de-duplicated in first-seen order. The change-gate options
	// fill it; the plan wire carries it on the op's watch field so
	// destination apply rebuilds the same gate.
	Watch []string
}

// SetChangeWatch arms the change gate and adds ids to the watched
// resources. Multiple calls accumulate; an id already watched is not added
// again, so callers never need to de-duplicate their watch lists. An empty
// ids arms the gate without adding a watch (the legacy IfChanged option,
// which only daemon-reload accepts: it then watches its WithWatch ids or,
// without any, its DependsOn ids). It uses a pointer receiver
// so the mutation is visible to the embedding value, and implements
// opt.ChangeWatchable.
func (c *ChangeGate) SetChangeWatch(ids []string) {
	c.Gated = true
	c.AddWatch(ids)
}

// AddWatch adds ids to the watch list (skipping ids already watched)
// without arming the gate. Daemon-reload uses it for its DependsOn fallback
// and when merging declarations; no option reaches it, so a recipe cannot
// record watched ids without arming the gate.
func (c *ChangeGate) AddWatch(ids []string) {
	for _, id := range ids {
		if !slices.Contains(c.Watch, id) {
			c.Watch = append(c.Watch, id)
		}
	}
}

// CheckWatch reports an armed gate with nothing to watch: it could never
// fire, so the gated action would be held forever. Daemon-reload's Ensure
// runs it after resolving its watch list and returns the error; its
// Present does not (a bare IfChanged may still merge with a same-bus
// declaration that names ids; one that stays unwatchable is refused by the
// plan pre-flight). The other gated kinds cannot be armed without ids through any
// option (OnChange and WatchChanges abort on an empty list, and the legacy
// IfChanged/WithWatch are daemon-reload-only), so they need no check. The
// embed must not implement SetWatch: that would make every embedder
// opt.ChangeGated and accept the legacy spellings.
func (c *ChangeGate) CheckWatch() error {
	if c.Gated && len(c.Watch) == 0 {
		return errNothingToWatch
	}
	return nil
}

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
	return c.Gated && !changed(c.Watch...)
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
