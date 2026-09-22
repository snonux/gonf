// Package systemd implements Linux systemd manager helpers shared by Service
// and Timer workflows: the systemctl client (see client.go) and the
// daemon-reload resource.
package systemd

import (
	"fmt"
	"slices"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// Interface assertions. resource.Register takes a DaemonReloadResource as a
// resource.Applier, so that contract is pinned here too: a renamed or
// re-signed Apply is reported at the declaration rather than at the Register
// call.
var (
	_ resource.Applier    = (*DaemonReloadResource)(nil)
	_ opt.UserService     = (*DaemonReloadResource)(nil)
	_ opt.Dependable      = (*DaemonReloadResource)(nil)
	_ opt.ChangeWatchable = (*DaemonReloadResource)(nil)
	_ opt.ChangeGated     = (*DaemonReloadResource)(nil)
)

// DaemonReloadResource runs systemctl daemon-reload (optionally --user).
// The embedded ChangeGate holds its one watch list: every change-gate
// option (OnChange, WatchChanges and the legacy IfChanged/WithWatch
// spellings) arms it through SetChangeWatch. Unlike the other gated kinds a
// reload armed without watched ids watches its DependsOn ids instead (the
// legacy IfChanged fallback, resolved once by newReload).
type DaemonReloadResource struct {
	embed.DependsOn
	embed.ChangeGate
	user bool
}

// Present registers a daemon-reload resource. The resource is a singleton
// per systemd bus and recipe scope (its ID is DaemonReload[system] or
// DaemonReload[user]): when the scope already registered a reload on the
// same bus — a second SystemdUnits composition, or an explicit DaemonReload
// next to one — the new declaration merges into the existing one (see
// mergeInto) and the existing resource is returned, so every caller depends
// on the one reload that watches all of their inputs.
func Present(opts ...opt.DaemonReloadOption) resource.Resource {
	d, err := newReload(opts)
	if err != nil {
		logger.Fatal("%s: %v", d.id(), err)
	}
	if r, prev, ok := registeredReload(d.id()); ok {
		return prev.mergeInto(r, d)
	}
	name := "system"
	if d.user {
		name = "user"
	}
	r := resource.Register("DaemonReload", name, d, d.DependsOn.IDs...)
	resource.RecordPlanDraft(d.planDraft(r.ID()))
	return r
}

// Ensure applies daemon-reload without registering or recording a plan draft.
func Ensure(opts ...opt.DaemonReloadOption) error {
	d, err := newReload(opts)
	if err != nil {
		return fmt.Errorf("%s: %w", d.id(), err)
	}
	return d.apply()
}

// newReload builds a daemon-reload with opts applied and its watch list
// resolved: when no option named watched ids, the reload watches its
// DependsOn ids (first seen first), armed or not. The fallback is filled in
// once, after every option ran, so option order does not matter and every
// later reader (apply, planDraft, merging) sees one list. An armed reload
// that still watches nothing (IfChanged with no WithWatch and no DependsOn)
// could never reload and is refused.
func newReload(opts []opt.DaemonReloadOption) (*DaemonReloadResource, error) {
	d := &DaemonReloadResource{}
	for _, o := range opts {
		o.Apply(d)
	}
	if len(d.Watch) == 0 {
		d.AddWatch(d.DependsOn.IDs)
	}
	return d, d.CheckWatch()
}

// SetUser implements opt.UserService (the WithUser option): the reload runs
// systemctl --user daemon-reload on the user manager instead of the system
// one, and the resource ID becomes DaemonReload[user].
func (d *DaemonReloadResource) SetUser() { d.user = true }

// WatchesDependsOn implements opt.DependsOnWatcher (and so, with the
// embedded SetChangeWatch, opt.ChangeGated): a reload armed without watched
// ids watches its DependsOn ids (resolved by newReload). It is what admits
// the legacy IfChanged and WithWatch options, which rely on that fallback.
func (d *DaemonReloadResource) WatchesDependsOn() {}

// Apply runs the daemon-reload reconciliation directly for the legacy resource path.
func (d *DaemonReloadResource) Apply() error { return d.apply() }

// planDraft records the daemon-reload op. Unlike the other gated kinds it
// does not use ChangeGate.DraftGate: Watch (including the DependsOn
// fallback) is recorded even when the gate is unarmed. Recorded plans
// already carry that shape, so it is kept for byte-stable plans, but the
// unarmed Watch is inert: destination apply (planHandler.Apply) reads
// op.Watch only when op.IfChanged is set and ignores it otherwise.
func (d *DaemonReloadResource) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:      "daemon_reload",
		ID:        id,
		User:      d.user,
		IfChanged: d.Gated,
		Watch:     slices.Clone(d.Watch),
		Deps:      d.DependsOn.SortedIDs(),
	}
}

func (d *DaemonReloadResource) apply() error {
	id := d.id()
	if err := Require("DaemonReload"); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}

	if d.Holds(resource.AnyChanged) {
		resource.Note(id, resource.StatusSkipped)
		logger.Debug("%s: skipped (no watched dependency changed)", id)
		return nil
	}

	// A single action with the id-wrapped error, so it goes through
	// resource.Mutate rather than resource.Converge. Its text still comes
	// from the shared Describe, and Mutate and Converge share the dry-run
	// prefix, so both log lines match Timer's and Service's wording.
	args := Args(d.user, "daemon-reload")
	would, did := Describe(args)
	return resource.Mutate(id, would, func() error {
		if err := Run(args...); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		logger.Info("%s", did)
		return nil
	})
}

func (d *DaemonReloadResource) id() string {
	name := "system"
	if d.user {
		name = "user"
	}
	return resource.FormatID("DaemonReload", name)
}
