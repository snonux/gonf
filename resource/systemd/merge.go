package systemd

import (
	"fmt"
	"slices"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// registeredReload returns the daemon-reload already registered under id in
// the current recipe scope, if any. Anything else registered under a
// DaemonReload[...] ID is not a reload to merge into; Present then registers
// as usual and the duplicate-ID declaration error reports the clash.
func registeredReload(id string) (resource.Resource, *DaemonReloadResource, bool) {
	r, applier, ok := resource.Registered(id)
	if !ok {
		return resource.Resource{}, nil, false
	}
	prev, ok := applier.(*DaemonReloadResource)
	return r, prev, ok
}

// merged returns d with next folded in, leaving d itself untouched so a
// refused merge changes nothing:
//
//   - Watch becomes the union of both watch lists (first seen first). Each
//     declaration's DependsOn fallback was already resolved into its Watch
//     (newReload), so a legacy IfChanged that relied on the fallback keeps
//     watching those ids once explicit ids join.
//   - DependsOn grows by next's ordering deps (orderingDeps: its DependsOn
//     ids plus the ids it watches), so the reload applies after every
//     declaration's inputs, including ones a declaration only watches.
//   - The gate stays armed only when both declarations are armed. An unarmed
//     declaration asks for an unconditional reload, and "always" absorbs
//     "only on change": reloading more often is harmless, while arming it
//     would silently skip a reload its author required. Activations keep
//     their own gates, so they still restart only on their own inputs.
func (d *DaemonReloadResource) merged(next *DaemonReloadResource) DaemonReloadResource {
	m := *d
	m.Gated = d.Gated && next.Gated
	m.Watch = slices.Clone(d.Watch) // m must not share d's backing array
	m.AddWatch(next.Watch)
	m.DependsOn.IDs = slices.Concat(d.DependsOn.IDs, next.orderingDeps())
	return m
}

// orderingDeps returns the ids a declaration folded into an existing reload
// must be ordered after: its DependsOn ids plus everything registered in
// this recipe scope whose change can fire its watch list
// (resource.RegisteredWatchTargets: each watched id, and for a watched
// Directory[p] also every File[p/…], the same prefix rule AnyChanged
// applies). On its own a reload keeps its recorded position (single
// declarations are unchanged), but a merged reload keeps the FIRST
// declaration's position, which lies before a later declaration's inputs.
// Without these edges the gated reload could run before such an input
// changed and be skipped: a watch-only input (WatchChanges, or the legacy
// WithWatch + IfChanged, adds no dep), or a file under a watched directory that only
// depends on the directory. Watched ids not registered in this scope come
// from an earlier scope, are recorded before the reload anyway, and cannot
// be an edge of the repository graph.
func (d *DaemonReloadResource) orderingDeps() []string {
	deps := slices.Clone(d.DependsOn.IDs)
	for _, id := range resource.RegisteredWatchTargets(d.Watch...) {
		if !slices.Contains(deps, id) {
			deps = append(deps, id)
		}
	}
	return deps
}

// mergeInto folds the new declaration next into d, the reload registered as
// r in this recipe scope, and returns r. resource.AmendRegistered checks the
// merged draft first and only then applies it everywhere: the registered
// draft (what api.Apply lowers) and dependency edges (what later
// amendments' cycle check walks) and,
// in plan-record mode, the op d already recorded, which the session's amend
// sink re-lowers through the normal draft lowering and replaces in place
// (plan.AmendRecorded) instead of recording a second reload. d itself is
// updated only after that succeeded.
//
// The merge is refused with an error naming both declarations by their
// watch lists (typically two SystemdUnits compositions), instead of
// recording a plan that could skip the reload or that no host can apply,
// when:
//   - a new ordering dep (a DependsOn or watched id of next) is the reload
//     itself or already depends on it (e.g. an input declared with
//     DependsOn(an earlier composition)), which would close a cycle;
//   - a when_* boundary or a privilege change lies between the recorded op
//     and this declaration, or this declaration runs under another
//     privilege than the recorded op (e.g. the reload came from a
//     Privileged nested Run): the recorded op can only absorb deps and
//     watches on resources recorded after it in the same when-block and
//     privilege chunk;
//   - a resource that joined the reload earlier (JoinRegisteredReload, a
//     SystemdTimer) references a unit the merged inputs may define
//     (refuseRelatedJoiner): the joiner is converged before the reload, so
//     it could start that unit from a stale definition, and its edge cannot
//     be removed again.
//
// A refusal is a declaration error (internal/declerr, surfaced by RecordPlan,
// Run, Apply and the CLI): d, the registered draft and the recorded op stay
// as they were, and r is still returned so the recipe keeps running.
func (d *DaemonReloadResource) mergeInto(r resource.Resource, next *DaemonReloadResource) resource.Resource {
	id := r.ID()
	m := d.merged(next)
	if err := m.refuseRelatedJoiner(id, next, d.Watch); err != nil {
		declerr.Report(err)
		return r
	}
	if err := resource.AmendRegistered(m.planDraft(id), next.orderingDeps()...); err != nil {
		declerr.Reportf("%s: cannot merge a further daemon-reload declaration on this bus (SystemdUnits FanIn or DaemonReload watching %v) into the one already declared in this recipe scope (watching %v): %v; "+
			"declare them in the same when-block and privilege scope without making one's inputs depend on the other, "+
			"or pass every input to a single SystemdUnits FanIn",
			id, next.Watch, d.Watch, err)
		return r
	}
	*d = m
	logger.Debug("%s: merged a further declaration on this bus; now watching %v", id, d.Watch)
	return r
}

// refuseRelatedJoiner returns the error that refuses the merge of next into
// the reload id (d is the merged reload, prevWatch the watch list before the
// merge) when one of d's joiners references a unit that its inputs may define. JoinRegisteredReload
// made the same check against the inputs known at join time; a later
// declaration can add such an input (bb2). The error names both watch lists
// like the other merge refusals, plus the joiner and the unit.
func (d *DaemonReloadResource) refuseRelatedJoiner(id string, next *DaemonReloadResource, prevWatch []string) error {
	j, unit, ok := d.joinerRelatedInput()
	if !ok {
		return nil
	}
	return fmt.Errorf("%s: cannot merge a further daemon-reload declaration on this bus (SystemdUnits FanIn or DaemonReload watching %v) into the one already declared in this recipe scope (watching %v): "+
		"%s, declared in between, shares that reload and applies before it, but references %s, which the merged reload's inputs may define, so it could be started from a stale unit definition; "+
		"declare %s after every same-bus SystemdUnits composition and DaemonReload, or pass every input to the SystemdUnits FanIn declared before it",
		id, next.Watch, prevWatch, j.id, unit, j.id)
}
