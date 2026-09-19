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
type DaemonReloadResource struct {
	embed.DependsOn
	user      bool
	ifChanged bool
	watch     []string // optional explicit watch list (plan apply); else DependsOn.IDs
}

func (d *DaemonReloadResource) SetUser()      { d.user = true }
func (d *DaemonReloadResource) SetIfChanged() { d.ifChanged = true }

// SetWatch overrides the watched resource ids for IfChanged; when unset the
// DependsOn ids are watched.
func (d *DaemonReloadResource) SetWatch(ids []string) {
	d.watch = append([]string(nil), ids...)
}

var (
	_ opt.UserService = (*DaemonReloadResource)(nil)
	_ opt.Dependable  = (*DaemonReloadResource)(nil)
	_ opt.ChangeGated = (*DaemonReloadResource)(nil)
	_ opt.Watchable   = (*DaemonReloadResource)(nil)
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
	r := resource.Register("DaemonReload", name,
		resource.ApplierFunc(func() error { return d.apply() }), d.DependsOn.IDs...)
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

func (d *DaemonReloadResource) planDraft(id string) resource.PlanDraft {
	watch := d.watch
	if len(watch) == 0 {
		watch = append([]string(nil), d.DependsOn.IDs...)
	}
	return resource.PlanDraft{
		Kind:      "daemon_reload",
		ID:        id,
		User:      d.user,
		IfChanged: d.ifChanged,
		Watch:     watch,
		Deps:      d.DependsOn.SortedIDs(),
	}
}

func (d *DaemonReloadResource) apply() error {
	id := d.id()
	if err := Require("DaemonReload"); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}

	if d.ifChanged {
		watch := d.watch
		if len(watch) == 0 {
			watch = d.DependsOn.IDs
		}
		if !resource.AnyChanged(watch...) {
			resource.Note(id, resource.StatusSkipped)
			logger.Debug("%s: skipped (no watched dependency changed)", id)
			return nil
		}
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

func (d *DaemonReloadResource) id() string {
	name := "system"
	if d.user {
		name = "user"
	}
	return fmt.Sprintf("DaemonReload[%s]", name)
}
