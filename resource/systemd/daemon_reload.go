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
// The embedded ChangeGate holds its one watch list: OnChange, WatchChanges
// and the legacy IfChanged arm it through SetChangeWatch. newReload then
// resolves the legacy parts once, after every option ran: the legacy
// WithWatch ids (legacyWatch, only an option-time slot) are appended after
// the gate's ids, and a reload that still watches nothing watches its
// DependsOn ids. Every later reader sees the one resolved Watch list.
//
// joiners are the resources ordered before this registered reload by
// JoinRegisteredReload (SystemdTimers), each with the units it references.
// They are registration-time bookkeeping only (never recorded in the plan):
// mergeInto re-checks them against every later declaration's inputs.
type DaemonReloadResource struct {
	embed.DependsOn
	embed.ChangeGate
	user        bool
	legacyWatch []string // WithWatch ids until newReload folds them into Watch
	joiners     []reloadJoiner
}

// reloadJoiner is one successful JoinRegisteredReload: the joiner's ID and
// the related units (After=/Wants= entries) it passed.
type reloadJoiner struct {
	id      string
	related []string
}

// Present registers a daemon-reload resource. The resource is a singleton
// per systemd bus and recipe scope (its ID is DaemonReload[system] or
// DaemonReload[user]): when the scope already registered a reload on the
// same bus — a second SystemdUnits composition, or an explicit DaemonReload
// next to one — the new declaration merges into the existing one (see
// mergeInto) and the existing resource is returned, so every caller depends
// on the one reload that watches all of their inputs. A SystemdTimer
// declared later on the same bus does not merge (its reload is part of its
// own op) but orders the registered reload after itself, so the two share
// one reload at apply time (see JoinRegisteredReload).
//
// Present does not run CheckWatch: a declaration armed with nothing to
// watch (a bare IfChanged) is valid while it can still merge with a
// same-bus declaration that names watched ids, before or after it. A
// reload that stays unwatchable is refused before anything is applied by
// the plan pre-flight (plan.ValidateChangeGates: "change-gated but
// watches nothing"), as it always was.
func Present(opts ...opt.DaemonReloadOption) resource.Resource {
	d := newReload(opts)
	if r, prev, ok := registeredReload(d.id()); ok {
		return prev.mergeInto(r, d)
	}
	r := resource.Register("DaemonReload", busName(d.user), d, d.DependsOn.IDs...)
	resource.RecordPlanDraft(d.planDraft(r.ID()))
	return r
}

// Ensure applies daemon-reload without registering or recording a plan
// draft. Nothing can merge into it, so a reload armed with nothing to watch
// (IfChanged with no WithWatch ids and no DependsOn) could never reload and
// is refused (CheckWatch) instead of being skipped.
func Ensure(opts ...opt.DaemonReloadOption) error {
	d := newReload(opts)
	if err := d.CheckWatch(); err != nil {
		return fmt.Errorf("%s: %w", d.id(), err)
	}
	return d.apply()
}

// newReload builds a daemon-reload with opts applied and its watch list
// resolved in the order recorded plans have always carried: the
// OnChange/WatchChanges ids first, then the legacy WithWatch ids, and when
// neither named any, the DependsOn ids (first seen first), armed or not.
// Resolving once, after every option ran, makes the list independent of
// option order and lets apply, planDraft and merging read one list.
func newReload(opts []opt.DaemonReloadOption) *DaemonReloadResource {
	d := &DaemonReloadResource{}
	for _, o := range opts {
		o.Apply(d)
	}
	d.AddWatch(d.legacyWatch)
	d.legacyWatch = nil
	if len(d.Watch) == 0 {
		d.AddWatch(d.DependsOn.IDs)
	}
	return d
}

// SetUser implements opt.UserService (the WithUser option): the reload runs
// systemctl --user daemon-reload on the user manager instead of the system
// one, and the resource ID becomes DaemonReload[user].
func (d *DaemonReloadResource) SetUser() { d.user = true }

// SetWatch implements opt.Watchable (the legacy WithWatch option) and so,
// with the embedded SetChangeWatch, opt.ChangeGated (IfChanged). The ids
// replace those of an earlier WithWatch; newReload appends them after the
// gate's own ids. It lives here rather than on embed.ChangeGate so that
// only daemon-reload accepts the legacy spellings.
func (d *DaemonReloadResource) SetWatch(ids []string) {
	d.legacyWatch = slices.Clone(ids)
}

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

	if d.Holds(d.changedSinceLastReload) {
		resource.Note(id, resource.StatusSkipped)
		logger.Debug("%s: skipped (no watched dependency changed since the last reload on this bus)", id)
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

// changedSinceLastReload is the oracle of the reload's change gate: a
// watched id changed after the last reload on this bus in this apply
// (resource.ChangedSince, anchored on this bus's DaemonReload ID, which the
// registered reload and every private one, e.g. SystemdTimer's, note
// under). A change noted before that reload is already loaded by the
// manager, so a gated reload whose inputs all changed earlier is redundant
// and held: that is how a SystemdTimer ordered before a same-bus
// SystemdUnits reload (see JoinRegisteredReload) shares one reload with
// it. Without an earlier reload on the bus it is resource.AnyChanged. An
// unarmed reload never consults the gate and always reloads.
func (d *DaemonReloadResource) changedSinceLastReload(ids ...string) bool {
	return resource.ChangedSince(d.id(), ids...)
}

func (d *DaemonReloadResource) id() string { return busReloadID(d.user) }

// busReloadID is the per-bus singleton ID of the daemon-reload:
// DaemonReload[user] for the user manager, DaemonReload[system] otherwise.
func busReloadID(user bool) string {
	return resource.FormatID("DaemonReload", busName(user))
}

// busName is the ID name of the system or user manager's reload.
func busName(user bool) string {
	if user {
		return "user"
	}
	return "system"
}
