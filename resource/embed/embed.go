// Package embed holds state common to all concrete resource types (dependency
// tracking, absence marking, and change gating), embedded rather than
// redeclared.
package embed

import "sort"

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
// The gate arms at registration time and is consulted at apply time via
// resource.AnyChanged: a gated action runs only when one of the watched
// resources reported a change (or would change, under dry-run) during the
// current apply.
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
