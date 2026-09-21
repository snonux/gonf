// Package systemd implements Linux systemd manager helpers shared by Service
// and Timer workflows: the systemctl client (see client.go) and the
// daemon-reload resource.
package systemd

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// DaemonReloadResource runs systemctl daemon-reload (optionally --user).
// The embedded ChangeGate supplies OnChange arming (SetChangeWatch); the
// legacy IfChanged option arms it through SetIfChanged below, which only
// daemon-reload implements.
type DaemonReloadResource struct {
	embed.DependsOn
	embed.ChangeGate
	user        bool
	legacyWatch []string // WithWatch target ids; merged with OnChange watches
}

func (d *DaemonReloadResource) SetUser() { d.user = true }

// SetIfChanged implements opt.ChangeGated (the legacy IfChanged option) by
// arming the embedded gate. It lives here rather than on embed.ChangeGate so
// that only daemon-reload accepts IfChanged; other gated resources reject it.
func (d *DaemonReloadResource) SetIfChanged() { d.Arm() }

// SetWatch sets the explicit legacy IfChanged watch ids. They are merged with
// OnChange targets so composing legacy WithWatch and OnChange is order
// independent; when neither form supplies ids, DependsOn ids are watched.
func (d *DaemonReloadResource) SetWatch(ids []string) {
	d.legacyWatch = append([]string(nil), ids...)
}

var (
	_ opt.UserService     = (*DaemonReloadResource)(nil)
	_ opt.Dependable      = (*DaemonReloadResource)(nil)
	_ opt.ChangeGated     = (*DaemonReloadResource)(nil)
	_ opt.Watchable       = (*DaemonReloadResource)(nil)
	_ opt.ChangeWatchable = (*DaemonReloadResource)(nil)
)

// Present registers a daemon-reload resource.
func Present(opts ...opt.DaemonReloadOption) resource.Resource {
	d := &DaemonReloadResource{}
	for _, o := range opts {
		o.Apply(d)
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
	d := &DaemonReloadResource{}
	for _, o := range opts {
		o.Apply(d)
	}
	return d.apply()
}

// planDraft records the daemon-reload op. Unlike the other gated kinds it
// does not use ChangeGate.DraftGate: Watch is the effective merged list
// (watchIDs) and is recorded even when the gate is unarmed. Recorded plans
// already carry that shape, so it is kept for byte-stable plans, but the
// unarmed Watch is inert: destination apply (planHandler.Apply) reads
// op.Watch only when op.IfChanged is set and ignores it otherwise.
func (d *DaemonReloadResource) planDraft(id string) resource.PlanDraft {
	watch := d.watchIDs()
	return resource.PlanDraft{
		Kind:      "daemon_reload",
		ID:        id,
		User:      d.user,
		IfChanged: d.Gated,
		Watch:     watch,
		Deps:      d.DependsOn.SortedIDs(),
	}
}

// Apply runs the daemon-reload reconciliation directly for the legacy resource path.
func (d *DaemonReloadResource) Apply() error { return d.apply() }

func (d *DaemonReloadResource) apply() error {
	id := d.id()
	if err := Require("DaemonReload"); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}

	// The gate consults the effective watch list (watchIDs), not only the
	// embed's OnChange ids, hence HoldsWatching rather than Holds.
	if d.HoldsWatching(resource.AnyChanged, d.watchIDs()) {
		resource.Note(id, resource.StatusSkipped)
		logger.Debug("%s: skipped (no watched dependency changed)", id)
		return nil
	}

	args := Args(d.user, "daemon-reload")

	return resource.Mutate(id, fmt.Sprintf("run systemctl %v", args), func() error {
		if err := Run(args...); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		logger.Info("systemctl %v", args)
		return nil
	})
}

// watchIDs combines OnChange and legacy WithWatch targets. Keeping the
// legacy list separate means WithWatch cannot accidentally erase an earlier
// OnChange target (or vice versa); both forms describe resources whose change
// should cause the same daemon reload. A legacy-only IfChanged retains its
// historical fallback to DependsOn IDs.
func (d *DaemonReloadResource) watchIDs() []string {
	watch := uniqueWatchIDs(d.Watch, d.legacyWatch)
	if len(watch) == 0 {
		watch = uniqueWatchIDs(d.DependsOn.IDs)
	}
	return watch
}

func uniqueWatchIDs(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, group := range groups {
		for _, id := range group {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func (d *DaemonReloadResource) id() string {
	name := "system"
	if d.user {
		name = "user"
	}
	return resource.FormatID("DaemonReload", name)
}
