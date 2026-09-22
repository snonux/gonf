package systemd

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// JoinRegisteredReload lets a resource that reloads the manager itself, as
// part of one composite op, share the bus's registered daemon-reload
// instead of adding a second reload to the apply. SystemdTimer is the
// caller: its op writes its unit files, reloads privately when they changed
// and only then converges the timer, so the reload cannot move out of the
// op. Instead the registered reload on the same bus (DaemonReload[user]
// for user, DaemonReload[system] otherwise; typically from SystemdUnits) is
// ordered after the joiner by one extra dependency edge on joinerID. The
// joiner then applies first, and its private reload also loads every
// input the registered reload watches that was written before it; the
// registered reload, whose gate only fires on a change noted after the
// bus's last reload (changedSinceLastReload), is then held unless another
// input changed later. So the bus reloads at most once for the inputs
// written before the joiner, and every unit is still loaded before it is
// activated: the registered reload's activations run after it, and the
// joiner's own enable/start/restart after its own reload. That joiner
// activation is no longer after the registered reload, though: when only
// the registered reload's inputs changed, the joiner restarts before they
// are reloaded. Its own units did not change then, which is why related
// (below) must not name one of those inputs. Only the dependency list of
// the recorded reload changes, so the plan schema does not.
//
// related are the units the joiner's units reference (SystemdTimer's
// After= and Wants=). Starting the joiner before the registered reload
// could otherwise start such a unit from a stale definition: e.g. a
// Persistent timer that fires on start, whose service wants a composition
// service whose changed unit file is only loaded by the later reload.
//
// It returns false, and leaves everything as it was (the joiner then keeps
// its private reload after the registered one, two reloads at worst, as
// before), when:
//   - no daemon-reload is registered on that bus in this recipe scope yet
//     (a SystemdTimer alone, one on the other bus, or one declared before
//     the reload: it then applies before the reload's inputs are written,
//     so its reload could not cover them anyway);
//   - a related unit may be one of the reload's inputs (mayManageUnit);
//   - resource.AmendRegistered refuses the edge: the joiner already
//     depends on the reload (DependsOn a SystemdUnits composition), which
//     would close a cycle, or a when-block boundary or privilege change
//     separates the recorded reload from the joiner.
//
// A registered reload that is not change-gated always reloads, so joining
// it only fixes the order; the joiner's own reload still runs on change.
func JoinRegisteredReload(user bool, joinerID string, related ...string) bool {
	id := busReloadID(user)
	r, d, ok := registeredReload(id)
	if !ok {
		return false
	}
	if slices.Contains(d.DependsOn.IDs, joinerID) {
		return true
	}
	if unit, ok := d.relatedInput(related); ok {
		logger.Debug("%s: %s keeps its own daemon-reload: it references %s, which may be an input of this bus's registered reload", id, joinerID, unit)
		return false
	}
	m := *d
	m.DependsOn.IDs = append(slices.Clone(d.DependsOn.IDs), joinerID)
	if err := resource.AmendRegistered(m.planDraft(r.ID()), joinerID); err != nil {
		logger.Debug("%s: %s keeps its own daemon-reload, not ordered before this bus's registered one: %v", id, joinerID, err)
		return false
	}
	*d = m
	logger.Debug("%s: ordered after %s, which shares this bus's reload", id, joinerID)
	return true
}

// relatedInput returns the first of units that one of d's inputs (its
// watched ids, and the registered files under a watched directory) may
// define, and whether there is one.
func (d *DaemonReloadResource) relatedInput(units []string) (string, bool) {
	if len(units) == 0 {
		return "", false
	}
	inputs := slices.Concat(d.Watch, resource.RegisteredWatchTargets(d.Watch...))
	for _, unit := range units {
		for _, in := range inputs {
			if mayManageUnit(in, unit) {
				return unit, true
			}
		}
	}
	return "", false
}

// mayManageUnit reports whether the resource id may install the unit file
// or a drop-in of unit. Only a File[path] can be judged by name: its base
// name is the unit (or its template, a@.service for a@x.service), or its
// parent directory is the unit's drop-in directory (unit.d, or the
// template's). Any other kind, including a Directory (a SyncDir's files are
// not registered one by one), may hold any unit and counts as managing it:
// the check must never let a join start a unit from a stale definition.
func mayManageUnit(id, unit string) bool {
	kind, path, ok := strings.Cut(strings.TrimSuffix(id, "]"), "[")
	if !ok || kind != "File" {
		return true
	}
	names := []string{unit}
	if tmpl, ok := templateName(unit); ok {
		names = append(names, tmpl)
	}
	base, parent := filepath.Base(path), filepath.Base(filepath.Dir(path))
	for _, n := range names {
		if base == n || parent == n+".d" {
			return true
		}
	}
	return false
}

// templateName returns the template unit of an instance name
// (a@x.service -> a@.service), and false for a unit that is no instance.
func templateName(unit string) (string, bool) {
	at := strings.IndexByte(unit, '@')
	dot := strings.LastIndexByte(unit, '.')
	if at < 0 || dot < at+2 {
		return "", false
	}
	return unit[:at+1] + unit[dot:], true
}
