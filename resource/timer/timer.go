// Package timer implements a systemd timer resource for Linux (Fedora and
// other systemd hosts). It enables/starts or stops/disables .timer units via
// systemctl, including the --user bus.
package timer

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
)

// Timer manages a named systemd .timer unit.
type Timer struct {
	embed.DependsOn
	embed.Absence
	name       string // unit name ending in .timer
	restart    bool
	user       bool // systemctl --user
	enableOnly bool // enable/disable only; skip start/stop
}

func (t *Timer) SetRestart()    { t.restart = true }
func (t *Timer) SetUser()       { t.user = true }
func (t *Timer) SetEnableOnly() { t.enableOnly = true }

var (
	_ opt.Absentable     = (*Timer)(nil)
	_ opt.Restartable    = (*Timer)(nil)
	_ opt.UserService    = (*Timer)(nil)
	_ opt.EnableOnlyable = (*Timer)(nil)
	_ opt.Dependable     = (*Timer)(nil)
)

// Present registers a timer that should be active and enabled (or only
// enabled when WithEnableOnly is set).
func Present(name string, opts ...opt.Option) resource.Resource {
	t := &Timer{name: normalizeUnit(name)}
	for _, o := range opts {
		o(t)
	}
	r := resource.Register("Timer", t.name,
		resource.ApplierFunc(func() error { return t.apply() }), t.DependsOn.IDs...)
	resource.RecordPlanDraft(t.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a timer without registering or recording a draft.
func Ensure(name string, opts ...opt.Option) error {
	t := &Timer{name: normalizeUnit(name)}
	for _, o := range opts {
		o(t)
	}
	return t.apply()
}

// Absent registers a timer that should be stopped and disabled.
func Absent(name string, opts ...opt.Option) resource.Resource {
	opts = append(opts, opt.IsAbsent)
	return Present(name, opts...)
}

func (t *Timer) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:       "timer",
		ID:         id,
		Name:       t.name,
		Absent:     t.Absent,
		User:       t.user,
		EnableOnly: t.enableOnly,
	}
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

func (t *Timer) apply() error {
	id := fmt.Sprintf("Timer[%s]", t.name)
	if err := t.validate(); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	if err := requireSystemd(); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}

	active, err := isActive(t.name, t.user)
	if err != nil {
		return err
	}
	enabled, err := isEnabled(t.name, t.user)
	if err != nil {
		return err
	}

	var actions [][]string
	if t.Absent {
		if !t.enableOnly && active {
			actions = append(actions, ctlArgs(t.user, "stop", t.name))
		}
		if enabled {
			actions = append(actions, ctlArgs(t.user, "disable", t.name))
		}
	} else {
		if !enabled {
			actions = append(actions, ctlArgs(t.user, "enable", t.name))
		}
		if !t.enableOnly {
			if !active {
				actions = append(actions, ctlArgs(t.user, "start", t.name))
			} else if t.restart {
				actions = append(actions, ctlArgs(t.user, "restart", t.name))
			}
		}
	}

	if len(actions) == 0 {
		noteResult(id, false)
		return nil
	}

	if resource.DryRun() {
		for _, a := range actions {
			logger.Info("dry-run: would run systemctl %v", a)
		}
		noteResult(id, true)
		return nil
	}

	for _, a := range actions {
		if err := ctlRun(a...); err != nil {
			return err
		}
		logger.Info("systemctl %v", a)
	}
	noteResult(id, true)
	return nil
}

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

func requireSystemd() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("Timer is only supported on Linux systemd (GOOS=%s)", runtime.GOOS)
	}
	if !systemdPresent() {
		return errors.New("Timer requires systemd (systemctl not found)")
	}
	return nil
}

func systemdPresent() bool {
	if exists("/run/systemd/system") {
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

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ctlArgs(user bool, args ...string) []string {
	if user {
		return append([]string{"--user"}, args...)
	}
	return args
}

func isActive(name string, user bool) (bool, error) {
	args := ctlArgs(user, "is-active", "--quiet", name)
	_, _, code, err := runCmd("systemctl", args...)
	if err != nil {
		return false, fmt.Errorf("systemctl is-active %s: %w", name, err)
	}
	return code == 0, nil
}

func isEnabled(name string, user bool) (bool, error) {
	args := ctlArgs(user, "is-enabled", "--quiet", name)
	_, _, code, err := runCmd("systemctl", args...)
	if err != nil {
		return false, fmt.Errorf("systemctl is-enabled %s: %w", name, err)
	}
	return code == 0, nil
}

func ctlRun(args ...string) error {
	stdout, stderr, code, err := runCmd("systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %v: %w", args, err)
	}
	if code != 0 {
		return fmt.Errorf("systemctl %v failed (exit %d): %s%s", args, code, stdout, stderr)
	}
	return nil
}

func noteResult(id string, changed bool) {
	if resource.DryRun() {
		if changed {
			resource.Note(id, resource.StatusWouldChange)
		} else {
			resource.Note(id, resource.StatusOK)
		}
		return
	}
	if changed {
		resource.Note(id, resource.StatusChanged)
	} else {
		resource.Note(id, resource.StatusOK)
	}
}
