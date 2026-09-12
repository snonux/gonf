// Package systemd implements Linux systemd manager helpers shared by Service
// and Timer workflows (daemon-reload).
package systemd

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
)

// runCmd is swapped in unit tests.
var runCmd = exec.Run

// DaemonReloadResource runs systemctl daemon-reload (optionally --user).
type DaemonReloadResource struct {
	embed.DependsOn
	user      bool
	ifChanged bool
	watch     []string // optional explicit watch list (plan apply); else DependsOn.IDs
}

func (d *DaemonReloadResource) SetUser()          { d.user = true }
func (d *DaemonReloadResource) SetIfChanged()     { d.ifChanged = true }
func (d *DaemonReloadResource) SetWatch(ids []string) {
	d.watch = append([]string(nil), ids...)
}

var (
	_ opt.UserService  = (*DaemonReloadResource)(nil)
	_ opt.Dependable   = (*DaemonReloadResource)(nil)
	_ opt.ChangeGated  = (*DaemonReloadResource)(nil)
	_ opt.Watchable    = (*DaemonReloadResource)(nil)
)

// Present registers a daemon-reload resource.
func Present(opts ...opt.Option) resource.Resource {
	d := &DaemonReloadResource{}
	for _, o := range opts {
		o(d)
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
func Ensure(opts ...opt.Option) error {
	d := &DaemonReloadResource{}
	for _, o := range opts {
		o(d)
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
	}
}

func (d *DaemonReloadResource) apply() error {
	id := d.id()
	if err := requireSystemd(); err != nil {
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

	args := []string{"daemon-reload"}
	if d.user {
		args = []string{"--user", "daemon-reload"}
	}

	if resource.DryRun() {
		logger.Info("dry-run: would run systemctl %v", args)
		resource.Note(id, resource.StatusWouldChange)
		return nil
	}

	stdout, stderr, code, err := runCmd("systemctl", args...)
	if err != nil {
		return fmt.Errorf("%s: systemctl %v: %w", id, args, err)
	}
	if code != 0 {
		return fmt.Errorf("%s: systemctl %v failed (exit %d): %s%s", id, args, code, stdout, stderr)
	}
	logger.Info("systemctl %v", args)
	resource.Note(id, resource.StatusChanged)
	return nil
}

func (d *DaemonReloadResource) id() string {
	name := "system"
	if d.user {
		name = "user"
	}
	return fmt.Sprintf("DaemonReload[%s]", name)
}

func requireSystemd() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("DaemonReload is only supported on Linux systemd (GOOS=%s)", runtime.GOOS)
	}
	if !systemdPresent() {
		return errors.New("DaemonReload requires systemd (systemctl not found)")
	}
	return nil
}

func systemdPresent() bool {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return true
	}
	if _, err := os.Stat("/usr/bin/systemctl"); err == nil {
		return true
	}
	if _, err := os.Stat("/bin/systemctl"); err == nil {
		return true
	}
	return false
}