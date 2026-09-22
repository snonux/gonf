// Package timer implements a systemd timer resource for Linux (Fedora and
// other systemd hosts). It enables/starts or stops/disables .timer units via
// systemctl, including the --user bus.
package timer

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/systemd"
)

var (
	// Register takes the value as a resource.Applier; asserting it here reports a
	// renamed or re-signed Apply at the declaration, not at the Register call.
	_ resource.Applier    = (*Timer)(nil)
	_ opt.Absentable      = (*Timer)(nil)
	_ opt.Restartable     = (*Timer)(nil)
	_ opt.UserService     = (*Timer)(nil)
	_ opt.EnableOnlyable  = (*Timer)(nil)
	_ opt.Dependable      = (*Timer)(nil)
	_ opt.ChangeWatchable = (*Timer)(nil)
	_ opt.MisuseReporter  = (*Timer)(nil)
)

// Timer manages a named systemd .timer unit.
type Timer struct {
	embed.DependsOn
	embed.Absence
	embed.ChangeGate
	embed.Misuse
	name       string // unit name ending in .timer
	restart    bool
	user       bool // systemctl --user
	enableOnly bool // enable/disable only; skip start/stop
}

// SetRestart requests a restart of an already-active present timer on each
// apply (subject to the OnChange gate). It has no effect with SetEnableOnly.
func (t *Timer) SetRestart() { t.restart = true }

// SetUser targets the systemd --user manager instead of the system one.
func (t *Timer) SetUser() { t.user = true }

// SetEnableOnly limits convergence to enable/disable: the timer is never
// started, stopped or restarted.
func (t *Timer) SetEnableOnly() { t.enableOnly = true }

// Present registers a timer that should be active and enabled (or only
// enabled when WithEnableOnly is set). An option misuse is reported as a
// declaration error (resource.Refuse) and nothing is registered.
func Present(name string, opts ...opt.TimerOption) resource.Resource {
	t, err := build(name, opts)
	if err != nil {
		return resource.Refuse("Timer", normalizeUnit(name), err)
	}
	r := resource.Register("Timer", t.name, t, t.DependsOn.IDs...)
	resource.RecordPlanDraft(t.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a timer without registering or recording a draft.
func Ensure(name string, opts ...opt.TimerOption) error {
	t, err := build(name, opts)
	if err != nil {
		return err
	}
	return t.apply()
}

// build applies opts to a new Timer. An option misuse collected while
// applying them (embed.Misuse) is its error.
func build(name string, opts []opt.TimerOption) (*Timer, error) {
	t := &Timer{name: normalizeUnit(name)}
	for _, o := range opts {
		o.Apply(t)
	}
	if err := t.MisuseErr(); err != nil {
		return nil, err
	}
	return t, nil
}

// Absent registers a timer that should be stopped and disabled.
func Absent(name string, opts ...opt.TimerOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

// Apply runs the timer reconciliation directly for the legacy resource path.
func (t *Timer) Apply() error { return t.apply() }

// planDraft records t as a "timer" plan draft under id, including its
// dependencies and OnChange gate.
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
	d.IfChanged, d.Watch = t.DraftGate()
	return d
}

// normalizeUnit trims name and appends the .timer suffix when it is missing.
// An empty (or all-space) name stays empty so validate can reject it.
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

// apply probes the timer, derives the systemctl actions that converge it,
// and runs them (or only logs them under dry-run) through the shared
// resource.Converge runner, which also notes the result.
func (t *Timer) apply() error {
	id := resource.FormatID("Timer", t.name)
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

	actions, held := t.actions(id, active, enabled)
	return resource.Converge(id, actions, held)
}

// actions returns the ordered systemctl actions that move t from the
// probed state to its desired state. Absent stops (unless enable-only)
// before disabling; present enables before starting. held reports that the
// change gate suppressed the restart.
func (t *Timer) actions(id string, active, enabled bool) (actions []resource.Action, held bool) {
	if t.Absent {
		if !t.enableOnly && active {
			actions = append(actions, t.command("stop"))
		}
		if enabled {
			actions = append(actions, t.command("disable"))
		}
		return actions, false
	}
	if !enabled {
		actions = append(actions, t.command("enable"))
	}
	if t.enableOnly {
		return actions, false
	}
	switch {
	case !active:
		actions = append(actions, t.command("start"))
	case !t.restart:
		// Active and no restart requested: nothing more to do. Checked
		// before the gate so an armed gate without WithRestart reports ok,
		// not skipped.
	case t.Holds(resource.AnyChanged):
		// The gated restart only fires after a watched resource changed
		// (embed.ChangeGate.Holds); convergence above is never gated.
		t.LogHeld(id, "restart")
		held = true
	default:
		actions = append(actions, t.command("restart"))
	}
	return actions, held
}

// command returns the systemctl Action performing verb on t's unit, on the
// --user bus when WithUser is set.
func (t *Timer) command(verb string) resource.Action {
	return systemd.Command(systemd.Args(t.user, verb, t.name))
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
