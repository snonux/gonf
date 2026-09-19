// Package timer implements a systemd timer resource for Linux (Fedora and
// other systemd hosts). It enables/starts or stops/disables .timer units via
// systemctl, including the --user bus.
package timer

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/systemd"
)

// Timer manages a named systemd .timer unit.
type Timer struct {
	embed.DependsOn
	embed.Absence
	embed.ChangeGate
	name       string // unit name ending in .timer
	restart    bool
	user       bool // systemctl --user
	enableOnly bool // enable/disable only; skip start/stop
}

func (t *Timer) SetRestart()    { t.restart = true }
func (t *Timer) SetUser()       { t.user = true }
func (t *Timer) SetEnableOnly() { t.enableOnly = true }

var (
	_ opt.Absentable      = (*Timer)(nil)
	_ opt.Restartable     = (*Timer)(nil)
	_ opt.UserService     = (*Timer)(nil)
	_ opt.EnableOnlyable  = (*Timer)(nil)
	_ opt.Dependable      = (*Timer)(nil)
	_ opt.ChangeWatchable = (*Timer)(nil)
)

// Present registers a timer that should be active and enabled (or only
// enabled when WithEnableOnly is set).
func Present(name string, opts ...opt.TimerOption) resource.Resource {
	t := &Timer{name: normalizeUnit(name)}
	for _, o := range opts {
		o.Apply(t)
	}
	r := resource.Register("Timer", t.name, t, t.DependsOn.IDs...)
	resource.RecordPlanDraft(t.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a timer without registering or recording a draft.
func Ensure(name string, opts ...opt.TimerOption) error {
	t := &Timer{name: normalizeUnit(name)}
	for _, o := range opts {
		o.Apply(t)
	}
	return t.apply()
}

// Absent registers a timer that should be stopped and disabled.
func Absent(name string, opts ...opt.TimerOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

func (t *Timer) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:       "timer",
		ID:         id,
		Name:       t.name,
		Absent:     t.Absent,
		User:       t.user,
		Restart:    t.restart,
		EnableOnly: t.enableOnly,
		Deps:       t.DependsOn.SortedIDs(),
	}
	d.IfChanged = t.Gated
	if d.IfChanged {
		d.Watch = append([]string(nil), t.Watch...)
	}
	return d
}

func normalizeUnit(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	if strings.HasSuffix(name, ".timer") {
		return name
	}
	return name + ".timer"
}

// Apply runs the timer reconciliation directly for the legacy resource path.
func (t *Timer) Apply() error { return t.apply() }

func (t *Timer) apply() error {
	id := fmt.Sprintf("Timer[%s]", t.name)
	if err := t.validate(); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	if err := systemd.Require("Timer"); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}

	active, err := systemd.IsActive(t.name, t.user)
	if err != nil {
		return err
	}
	enabled, err := systemd.IsEnabled(t.name, t.user)
	if err != nil {
		return err
	}

	var actions [][]string
	held := false // change gate suppressed the restart action
	if t.Absent {
		if !t.enableOnly && active {
			actions = append(actions, systemd.Args(t.user, "stop", t.name))
		}
		if enabled {
			actions = append(actions, systemd.Args(t.user, "disable", t.name))
		}
	} else {
		if !enabled {
			actions = append(actions, systemd.Args(t.user, "enable", t.name))
		}
		if !t.enableOnly {
			if !active {
				actions = append(actions, systemd.Args(t.user, "start", t.name))
			} else if t.restart {
				// The gated restart only fires after a watched resource changed.
				if t.Gated && !resource.AnyChanged(t.Watch...) {
					logger.Debug("%s: restart held by change gate (no watched dependency changed)", id)
					held = true
				} else {
					actions = append(actions, systemd.Args(t.user, "restart", t.name))
				}
			}
		}
	}

	if len(actions) == 0 {
		if held {
			resource.Note(id, resource.StatusSkipped)
			return nil
		}
		resource.NoteResult(id, false)
		return nil
	}

	if resource.DryRun() {
		for _, a := range actions {
			logger.Info("dry-run: would run systemctl %v", a)
		}
		resource.NoteResult(id, true)
		return nil
	}

	for _, a := range actions {
		if err := systemd.Run(a...); err != nil {
			return err
		}
		logger.Info("systemctl %v", a)
	}
	resource.NoteResult(id, true)
	return nil
}

// validate is Timer-specific: unlike Service, timer unit names are checked
// before they reach systemctl (non-empty, no whitespace/path separators,
// .timer suffix). Service does not validate; that drift is deliberate for
// now and lives entirely at the callers, not in the shared client.
func (t *Timer) validate() error {
	if t.name == "" || t.name == ".timer" {
		return errors.New("name must not be empty")
	}
	if strings.ContainsAny(t.name, "/ \t\n\r") {
		return errors.New("name must not contain whitespace or path separators")
	}
	if !strings.HasSuffix(t.name, ".timer") {
		return errors.New("name must end with .timer")
	}
	return nil
}
