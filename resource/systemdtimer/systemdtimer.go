// Package systemdtimer implements a declarative systemd timer + oneshot
// service pair: it writes the unit files, daemon-reloads when they change,
// and enables/starts the timer (Linux/systemd).
package systemdtimer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/timer"
)

// SystemdTimer manages a named systemd .timer with a companion oneshot .service.
type SystemdTimer struct {
	embed.DependsOn
	embed.Absence
	name               string // unit name ending in .timer
	base               string // name without .timer
	command            string
	onCalendar         string
	onBootSec          string
	persistent         bool
	description        string
	serviceDescription string
	after              []string
	wants              []string
	user               bool
	restart            bool
	enableOnly         bool
}

func (t *SystemdTimer) SetCommand(cmd string)          { t.command = cmd }
func (t *SystemdTimer) SetOnCalendar(v string)         { t.onCalendar = v }
func (t *SystemdTimer) SetOnBootSec(v string)          { t.onBootSec = v }
func (t *SystemdTimer) SetPersistent()                 { t.persistent = true }
func (t *SystemdTimer) SetDescription(v string)        { t.description = v }
func (t *SystemdTimer) SetServiceDescription(v string) { t.serviceDescription = v }
func (t *SystemdTimer) AddAfter(units ...string)       { t.after = append(t.after, units...) }
func (t *SystemdTimer) AddWants(units ...string)       { t.wants = append(t.wants, units...) }
func (t *SystemdTimer) SetUser()                       { t.user = true }
func (t *SystemdTimer) SetRestart()                    { t.restart = true }
func (t *SystemdTimer) SetEnableOnly()                 { t.enableOnly = true }

var (
	_ opt.Absentable             = (*SystemdTimer)(nil)
	_ opt.Dependable             = (*SystemdTimer)(nil)
	_ opt.Commandable            = (*SystemdTimer)(nil)
	_ opt.OnCalendarable         = (*SystemdTimer)(nil)
	_ opt.OnBootSecable          = (*SystemdTimer)(nil)
	_ opt.Persistentable         = (*SystemdTimer)(nil)
	_ opt.Descriptionable        = (*SystemdTimer)(nil)
	_ opt.ServiceDescriptionable = (*SystemdTimer)(nil)
	_ opt.Afterable              = (*SystemdTimer)(nil)
	_ opt.Wantsable              = (*SystemdTimer)(nil)
	_ opt.UserService            = (*SystemdTimer)(nil)
	_ opt.Restartable            = (*SystemdTimer)(nil)
	_ opt.EnableOnlyable         = (*SystemdTimer)(nil)
)

// Present registers a systemd timer that should be installed, enabled, and
// started (or only enabled when WithEnableOnly is set).
func Present(name string, opts ...opt.SystemdTimerOption) resource.Resource {
	t := newTimer(name, opts...)
	r := resource.Register("SystemdTimer", t.base, t, t.DependsOn.IDs...)
	resource.RecordPlanDraft(t.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a systemd timer without registering or recording a draft.
func Ensure(name string, opts ...opt.SystemdTimerOption) error {
	return newTimer(name, opts...).apply()
}

// Absent registers a systemd timer whose units should be stopped, disabled, and removed.
func Absent(name string, opts ...opt.SystemdTimerOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

func newTimer(name string, opts ...opt.SystemdTimerOption) *SystemdTimer {
	base, unit := normalizeName(name)
	t := &SystemdTimer{name: unit, base: base}
	for _, o := range opts {
		o.Apply(t)
	}
	return t
}

func (t *SystemdTimer) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:               "systemd_timer",
		ID:                 id,
		Name:               t.base,
		Absent:             t.Absent,
		User:               t.user,
		Restart:            t.restart,
		EnableOnly:         t.enableOnly,
		Command:            t.command,
		OnCalendar:         t.onCalendar,
		OnBootSec:          t.onBootSec,
		Persistent:         t.persistent,
		Description:        t.description,
		ServiceDescription: t.serviceDescription,
		After:              append([]string(nil), t.after...),
		Wants:              append([]string(nil), t.wants...),
		Deps:               t.DependsOn.SortedIDs(),
	}
}

func normalizeName(name string) (base, unit string) {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".service")
	name = strings.TrimSuffix(name, ".timer")
	if name == "" {
		return "", ""
	}
	return name, name + ".timer"
}

// Apply runs the systemd timer reconciliation directly for the legacy resource path.
func (t *SystemdTimer) Apply() error { return t.apply() }

func (t *SystemdTimer) apply() error {
	id := fmt.Sprintf("SystemdTimer[%s]", t.base)
	if err := t.validate(); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	if err := systemd.Require("SystemdTimer"); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}

	dir, err := t.unitDir()
	if err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	svcPath := filepath.Join(dir, t.base+".service")
	timerPath := filepath.Join(dir, t.base+".timer")
	svcID := fmt.Sprintf("File[%s]", svcPath)
	timerFileID := fmt.Sprintf("File[%s]", timerPath)

	if t.Absent {
		if err := t.applyAbsent(svcPath, timerPath, svcID, timerFileID); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		resource.NoteResult(id, resource.AnyChanged(svcID, timerFileID,
			daemonReloadID(t.user), fmt.Sprintf("Timer[%s]", t.name)))
		return nil
	}

	if err := t.applyPresent(dir, svcPath, timerPath, svcID, timerFileID); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	resource.NoteResult(id, resource.AnyChanged(svcID, timerFileID,
		daemonReloadID(t.user), fmt.Sprintf("Timer[%s]", t.name)))
	return nil
}

func (t *SystemdTimer) applyPresent(dir, svcPath, timerPath, svcID, timerFileID string) error {
	if resource.DryRun() {
		logger.Info("dry-run: would ensure unit dir %s", dir)
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if err := file.Ensure(svcPath,
		opt.WithContent(t.serviceUnit()),
		opt.WithMode(0o644),
	); err != nil {
		return err
	}
	if err := file.Ensure(timerPath,
		opt.WithContent(t.timerUnit()),
		opt.WithMode(0o644),
	); err != nil {
		return err
	}

	reloadOpts := []opt.DaemonReloadOption{
		opt.IfChanged,
		opt.WithWatch(svcID, timerFileID),
	}
	if t.user {
		reloadOpts = append(reloadOpts, opt.WithUser)
	}
	if err := systemd.Ensure(reloadOpts...); err != nil {
		return err
	}

	timerOpts := []opt.TimerOption{}
	if t.user {
		timerOpts = append(timerOpts, opt.WithUser)
	}
	if t.restart {
		timerOpts = append(timerOpts, opt.WithRestart)
	}
	if t.enableOnly {
		timerOpts = append(timerOpts, opt.WithEnableOnly)
	}
	return timer.Ensure(t.name, timerOpts...)
}

func (t *SystemdTimer) applyAbsent(svcPath, timerPath, svcID, timerFileID string) error {
	timerOpts := []opt.TimerOption{opt.IsAbsent}
	if t.user {
		timerOpts = append(timerOpts, opt.WithUser)
	}
	if t.enableOnly {
		timerOpts = append(timerOpts, opt.WithEnableOnly)
	}
	// Best-effort stop/disable before removing unit files.
	if err := timer.Ensure(t.name, timerOpts...); err != nil {
		logger.Debug("SystemdTimer[%s]: stop/disable before remove: %v", t.base, err)
	}

	if err := file.Ensure(svcPath, opt.IsAbsent); err != nil {
		return err
	}
	if err := file.Ensure(timerPath, opt.IsAbsent); err != nil {
		return err
	}

	reloadOpts := []opt.DaemonReloadOption{
		opt.IfChanged,
		opt.WithWatch(svcID, timerFileID),
	}
	if t.user {
		reloadOpts = append(reloadOpts, opt.WithUser)
	}
	return systemd.Ensure(reloadOpts...)
}

func (t *SystemdTimer) unitDir() (string, error) {
	if !t.user {
		return "/etc/systemd/system", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config/systemd/user"), nil
}

func daemonReloadID(user bool) string {
	name := "system"
	if user {
		name = "user"
	}
	return fmt.Sprintf("DaemonReload[%s]", name)
}

func (t *SystemdTimer) serviceUnit() string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	desc := t.serviceDescription
	if desc == "" {
		desc = t.description
	}
	if desc == "" {
		desc = "gonf oneshot for " + t.base
	}
	fmt.Fprintf(&b, "Description=%s\n", desc)
	for _, w := range t.wants {
		fmt.Fprintf(&b, "Wants=%s\n", w)
	}
	for _, a := range t.after {
		fmt.Fprintf(&b, "After=%s\n", a)
	}
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=oneshot\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", t.command)
	return b.String()
}

func (t *SystemdTimer) timerUnit() string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	desc := t.description
	if desc == "" {
		desc = "Timer for " + t.base
	}
	fmt.Fprintf(&b, "Description=%s\n", desc)
	b.WriteString("\n[Timer]\n")
	if t.onBootSec != "" {
		fmt.Fprintf(&b, "OnBootSec=%s\n", t.onBootSec)
	}
	fmt.Fprintf(&b, "OnCalendar=%s\n", t.onCalendar)
	if t.persistent {
		b.WriteString("Persistent=true\n")
	}
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=timers.target\n")
	return b.String()
}

func (t *SystemdTimer) validate() error {
	if t.base == "" || t.name == "" || t.name == ".timer" {
		return errors.New("name must not be empty")
	}
	if strings.ContainsAny(t.base, "/ \t\n\r") {
		return errors.New("name must not contain whitespace or path separators")
	}
	if t.Absent {
		return nil
	}
	if strings.TrimSpace(t.command) == "" {
		return errors.New("WithCommand is required")
	}
	if strings.TrimSpace(t.onCalendar) == "" {
		return errors.New("WithOnCalendar is required")
	}
	return nil
}
