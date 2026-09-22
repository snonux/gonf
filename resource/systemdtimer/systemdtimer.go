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

var (
	// Register takes the value as a resource.Applier; asserting it here reports a
	// renamed or re-signed Apply at the declaration, not at the Register call.
	_ resource.Applier           = (*SystemdTimer)(nil)
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
	_ opt.Sensitivable           = (*SystemdTimer)(nil)
)

// ensureReload applies a daemon-reload; tests swap it to observe the options
// both apply paths hand it without running systemctl.
var ensureReload = systemd.Ensure

// SystemdTimer manages a named systemd .timer with a companion oneshot .service.
//
// The Sensitivity embed backs WithSensitive: the ExecStart command holds
// secret material, so the recorded op is sensitive and both unit files are
// written as sensitive Files (unitFileOptions). The command still ends up
// in the service unit (mode 0644) and in systemd's own status output; keep
// secrets in a 0600 file the command reads.
type SystemdTimer struct {
	embed.DependsOn
	embed.Absence
	embed.Sensitivity
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

// newTimer builds a SystemdTimer for name (with or without a .timer or
// .service suffix) and applies opts.
func newTimer(name string, opts ...opt.SystemdTimerOption) *SystemdTimer {
	base, unit := normalizeName(name)
	t := &SystemdTimer{name: unit, base: base}
	for _, o := range opts {
		o.Apply(t)
	}
	return t
}

// SetCommand sets the companion service's ExecStart= command (required for a
// present timer).
func (t *SystemdTimer) SetCommand(cmd string) { t.command = cmd }

// SetOnCalendar sets the timer's OnCalendar= expression (required for a
// present timer).
func (t *SystemdTimer) SetOnCalendar(v string) { t.onCalendar = v }

// SetOnBootSec sets the timer's OnBootSec= delay.
func (t *SystemdTimer) SetOnBootSec(v string) { t.onBootSec = v }

// SetPersistent writes Persistent=true into the timer unit.
func (t *SystemdTimer) SetPersistent() { t.persistent = true }

// SetDescription sets the timer unit's Description=. It is also the service
// description when SetServiceDescription is not used.
func (t *SystemdTimer) SetDescription(v string) { t.description = v }

// SetServiceDescription sets the companion service unit's Description=.
func (t *SystemdTimer) SetServiceDescription(v string) { t.serviceDescription = v }

// AddAfter appends units to the companion service's After= ordering.
func (t *SystemdTimer) AddAfter(units ...string) { t.after = append(t.after, units...) }

// AddWants appends units to the companion service's Wants= dependencies.
func (t *SystemdTimer) AddWants(units ...string) { t.wants = append(t.wants, units...) }

// SetUser installs the units under ~/.config/systemd/user and manages them
// on the systemd --user manager.
func (t *SystemdTimer) SetUser() { t.user = true }

// SetRestart restarts an already-active present timer on each apply.
func (t *SystemdTimer) SetRestart() { t.restart = true }

// SetEnableOnly limits the timer to enable/disable: it is never started,
// stopped or restarted.
func (t *SystemdTimer) SetEnableOnly() { t.enableOnly = true }

// Present registers a systemd timer that should be installed, enabled, and
// started (or only enabled when WithEnableOnly is set).
//
// When this recipe scope already registered a daemon-reload on the timer's
// bus (e.g. a SystemdUnits composition), Present orders that reload after
// the timer (systemd.JoinRegisteredReload): the timer's own change-gated
// reload then also loads the composition's earlier-written inputs, and the
// registered reload is held unless one of its inputs changed after it, so
// the bus reloads once for them. The timer is then converged before the
// composition's reload, so the join is refused when the companion service's
// After=/Wants= name a unit the composition may install (it could be
// started from a stale definition). A same-bus declaration after the
// timer that would add such a unit to the joined reload's inputs is then
// refused fail-fast when it merges. The timer's op is unchanged, and
// without such a reload (or when joining it is refused) the timer behaves
// exactly as a standalone one.
func Present(name string, opts ...opt.SystemdTimerOption) resource.Resource {
	t := newTimer(name, opts...)
	r := resource.Register("SystemdTimer", t.base, t, t.DependsOn.IDs...)
	resource.RecordPlanDraft(t.planDraft(r.ID()))
	systemd.JoinRegisteredReload(t.user, r.ID(), slices.Concat(t.after, t.wants)...)
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

// Apply runs the systemd timer reconciliation directly for the legacy resource path.
func (t *SystemdTimer) Apply() error { return t.apply() }

// planDraft records t as a "systemd_timer" plan draft under id.
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
		Sensitive:          t.Sensitive,
	}
}

// normalizeName trims name and strips a .service or .timer suffix, returning
// the bare base name and the .timer unit name (both empty for an empty name).
func normalizeName(name string) (base, unit string) {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".service")
	name = strings.TrimSuffix(name, ".timer")
	if name == "" {
		return "", ""
	}
	return name, name + ".timer"
}

// apply converges the unit files, daemon-reload and timer unit for the
// present or absent state, and reports the composite changed when any part
// changed.
func (t *SystemdTimer) apply() error {
	id := resource.FormatID("SystemdTimer", t.base)
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
	svcID := resource.FormatID("File", svcPath)
	timerFileID := resource.FormatID("File", timerPath)

	if t.Absent {
		err = t.applyAbsent(svcPath, timerPath, svcID, timerFileID)
	} else {
		err = t.applyPresent(dir, svcPath, timerPath, svcID, timerFileID)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	// The composite changed when any of its parts (unit files, the
	// daemon-reload, the timer unit) noted a change during this apply.
	resource.NoteResult(id, resource.AnyChanged(svcID, timerFileID,
		daemonReloadID(t.user), timerUnitID(t.name)))
	return nil
}

// ensureDaemonReload reloads the systemd manager only when one of the unit
// files changed. Shared by the present and absent paths so both gate the
// reload identically.
func (t *SystemdTimer) ensureDaemonReload(svcID, timerFileID string) error {
	return ensureReload(t.daemonReloadOpts(svcID, timerFileID)...)
}

// daemonReloadOpts is the daemon-reload configuration for t's unit files:
// the change gate (embed.ChangeGate on the daemon-reload resource) watching
// exactly the two unit files, on the user bus for WithUser timers.
func (t *SystemdTimer) daemonReloadOpts(svcID, timerFileID string) []opt.DaemonReloadOption {
	reloadOpts := []opt.DaemonReloadOption{
		opt.WatchChanges(svcID, timerFileID),
	}
	if t.user {
		reloadOpts = append(reloadOpts, opt.WithUser)
	}
	return reloadOpts
}

// applyPresent writes both unit files (creating dir), daemon-reloads when
// they changed and converges the timer unit via resource/timer.
func (t *SystemdTimer) applyPresent(dir, svcPath, timerPath, svcID, timerFileID string) error {
	if resource.DryRun() {
		logger.Info("dry-run: would ensure unit dir %s", dir)
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if err := file.Ensure(svcPath, t.unitFileOptions(t.serviceUnit())...); err != nil {
		return err
	}
	if err := file.Ensure(timerPath, t.unitFileOptions(t.timerUnit())...); err != nil {
		return err
	}

	if err := t.ensureDaemonReload(svcID, timerFileID); err != nil {
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

// applyAbsent stops and disables the timer (best effort), removes both unit
// files and daemon-reloads when they changed.
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
	return t.ensureDaemonReload(svcID, timerFileID)
}

// unitFileOptions are the File options a unit file with content is written
// with: mode 0644, and WithSensitive for a sensitive timer.
func (t *SystemdTimer) unitFileOptions(content string) []opt.FileOption {
	opts := []opt.FileOption{opt.WithContent(content), opt.WithMode(0o644)}
	if t.Sensitive {
		opts = append(opts, opt.WithSensitive)
	}
	return opts
}

// unitDir is the directory the unit files live in: /etc/systemd/system, or
// ~/.config/systemd/user for a user timer.
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

// timerUnitID is the ID the Timer resource (resource/timer) registers and
// reports its unit under; the composite watches it for changes.
func timerUnitID(name string) string { return resource.FormatID("Timer", name) }

// daemonReloadID is the ID the DaemonReload resource (resource/systemd)
// registers and reports under for the system or user manager.
func daemonReloadID(user bool) string {
	name := "system"
	if user {
		name = "user"
	}
	return resource.FormatID("DaemonReload", name)
}

// serviceUnit renders the companion oneshot .service unit.
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

// timerUnit renders the .timer unit.
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

// validate rejects an empty name or one containing whitespace or path
// separators, and requires WithCommand and WithOnCalendar for a present timer.
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
