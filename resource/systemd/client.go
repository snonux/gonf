// Shared systemctl client: every resource that shells out to systemctl
// (service, timer, DaemonReload, and SystemdTimer through them) routes
// through Client's methods instead of hand-rolling its own query/run
// wrappers. There are deliberately no package-level IsActive/IsEnabled/Run
// shortcuts (task pg2 removed them): they hard-coded the zero Client, so a
// call site reaching for one silently ignored the per-apply
// ctx.Runners.Systemd injection and, in a test, hit the host's real
// systemctl. A caller that truly wants the real runner writes Client{}
// explicitly, which keeps the bypass visible at the call site.

package systemd

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/resource"
)

// RunFunc is the systemctl invocation signature every helper in this
// package funnels through: internal/exec.Run's shape, or an override a
// *runners.SystemdRunners injects for one apply (task 4e2, replacing the
// internal/testseam.FakeSystemctl process-global fake this package used to
// consult).
type RunFunc func(name string, args ...string) (stdout, stderr string, exitCode int, err error)

// Client carries the systemctl runner override for one apply; the zero
// Client reaches the real internal/exec runner. Service (its systemd
// backend), Timer and DaemonReload each hold or build one with NewClient
// from their own injected *runners.SystemdRunners instead of a
// process-global fake, mirroring resource/cmd.Cmd.runFn from qb2;
// SystemdTimer reaches one through its Timer/DaemonReload composition.
type Client struct {
	run RunFunc
}

// NewClient builds a Client from sr (nil, or a nil sr.Run: the real
// runner). runners.SystemdOf(ctx.Runners) supplies sr in a plan apply.
func NewClient(sr *runners.SystemdRunners) Client {
	if sr == nil {
		return Client{}
	}
	return Client{run: sr.Run}
}

// Args builds a systemctl argument vector, prefixing --user when user selects
// the user bus. args are the operation and its operands.
func Args(user bool, args ...string) []string {
	if user {
		return append([]string{"--user"}, args...)
	}
	return args
}

// IsActive reports whether the unit is currently active through c's runner.
// A non-zero exit of systemctl is-active (unit inactive) is a normal false,
// not an error.
func (c Client) IsActive(name string, user bool) (bool, error) {
	args := Args(user, "is-active", "--quiet", name)
	_, _, code, err := c.runCmd("systemctl", args...)
	if err != nil {
		return false, fmt.Errorf("systemctl is-active %s: %w", name, err)
	}
	return code == 0, nil
}

// IsEnabled reports whether the unit is enabled at boot through c's runner
// (Enablement's Enabled). A non-zero exit of systemctl is-enabled (unit
// disabled) is a normal false, not an error.
func (c Client) IsEnabled(name string, user bool) (bool, error) {
	e, err := c.Enablement(name, user)
	return e.Enabled, err
}

// Enablement is a unit's probed boot-time enablement: what systemctl
// is-enabled printed and whether it exited 0.
type Enablement struct {
	// State is the state is-enabled printed ("enabled", "static",
	// "enabled-runtime", ...), trimmed; empty when it printed nothing.
	State string
	// Enabled is is-enabled's exit status 0. systemctl exits 0 not only for
	// "enabled" but also for "enabled-runtime", "static", "indirect",
	// "generated", "alias" and "transient": the unit needs no enable.
	Enabled bool
}

// Enablement probes the unit with systemctl is-enabled through c's runner.
// Unlike IsActive it does not pass --quiet: the printed state is what tells
// a unit disable can change from one it cannot (DisableOp).
func (c Client) Enablement(name string, user bool) (Enablement, error) {
	args := Args(user, "is-enabled", name)
	stdout, _, code, err := c.runCmd("systemctl", args...)
	if err != nil {
		return Enablement{}, fmt.Errorf("systemctl is-enabled %s: %w", name, err)
	}
	state, _, _ := strings.Cut(strings.TrimSpace(stdout), "\n")
	return Enablement{State: strings.TrimSpace(state), Enabled: code == 0}, nil
}

// DisableOp returns the systemctl operation (before the unit name, without
// --user) that removes e's boot-time enablement, or nil when there is none
// to remove. A unit whose enablement disable cannot change ("static",
// "indirect", "generated", "alias", "transient") gets nil: systemctl disable
// exits 0 for it without changing anything, so disabling it would report a
// change on every apply. A runtime enablement ("enabled-runtime") is only
// removed by disable --runtime. Any other state that exited 0 ("enabled",
// or no printed state) is disabled plainly.
func (e Enablement) DisableOp() []string {
	if !e.Enabled {
		return nil
	}
	switch e.State {
	case "static", "indirect", "generated", "alias", "transient":
		return nil
	case "enabled-runtime":
		return []string{"disable", "--runtime"}
	default:
		return []string{"disable"}
	}
}

// Run executes systemctl with args through c's runner and fails on a
// non-zero exit or when the command could not be started.
func (c Client) Run(args ...string) error {
	stdout, stderr, code, err := c.runCmd("systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %v: %w", args, err)
	}
	if code != 0 {
		return fmt.Errorf("systemctl %v failed (exit %d): %s%s", args, code, stdout, stderr)
	}
	return nil
}

// Command returns the resource.Action performing one systemctl invocation
// (build args with Args to get --user handling) through c's runner.
func (c Client) Command(args []string) Command {
	return Command{args: args, run: c.run}
}

// Require fails when systemd unit management is unavailable on this host:
// a non-Linux GOOS or no detectable systemctl. what names the calling
// feature in error messages (e.g. "Timer", "DaemonReload").
func Require(what string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%s is only supported on Linux systemd (GOOS=%s)", what, runtime.GOOS)
	}
	if !Detected() {
		return fmt.Errorf("%s requires systemd (systemctl not found)", what)
	}
	return nil
}

// Detected reports whether a systemd manager is detectable on this host: the
// /run/systemd/system marker directory or a systemctl binary in the usual
// locations.
func Detected() bool {
	if resource.Exists("/run/systemd/system") {
		return true
	}
	return resource.Exists("/usr/bin/systemctl") || resource.Exists("/bin/systemctl")
}

// runCmd executes a systemctl invocation through c.run when NewClient
// injected one, otherwise the real internal/exec runner. Every systemctl
// helper on Client routes through it.
func (c Client) runCmd(name string, args ...string) (string, string, int, error) {
	if c.run != nil {
		return c.run(name, args...)
	}
	return exec.Run(name, args...)
}
