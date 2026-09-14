// Shared systemctl client: every resource that shells out to systemctl
// (service, timer, DaemonReload) routes through these helpers instead of
// hand-rolling its own query/run wrappers.

package systemd

import (
	"fmt"
	"runtime"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
)

// runCmd executes an external command. Swapped in unit tests via
// SetRunCmdForTest; all systemctl helpers route through it.
var runCmd = exec.Run

// SetRunCmdForTest swaps the systemctl command runner (tests only). Service
// and Timer tests reach their systemctl paths through this seam.
func SetRunCmdForTest(run func(name string, args ...string) (string, string, int, error)) {
	runCmd = run
}

// ResetRunCmdForTest restores the real command runner.
func ResetRunCmdForTest() {
	runCmd = exec.Run
}

// Args builds a systemctl argument vector, prefixing --user when user selects
// the user bus. args are the operation and its operands.
func Args(user bool, args ...string) []string {
	if user {
		return append([]string{"--user"}, args...)
	}
	return args
}

// IsActive reports whether the unit is currently active. A non-zero exit of
// systemctl is-active (unit inactive) is a normal false, not an error.
func IsActive(name string, user bool) (bool, error) {
	args := Args(user, "is-active", "--quiet", name)
	_, _, code, err := runCmd("systemctl", args...)
	if err != nil {
		return false, fmt.Errorf("systemctl is-active %s: %w", name, err)
	}
	return code == 0, nil
}

// IsEnabled reports whether the unit is enabled at boot. A non-zero exit of
// systemctl is-enabled (unit disabled) is a normal false, not an error.
func IsEnabled(name string, user bool) (bool, error) {
	args := Args(user, "is-enabled", "--quiet", name)
	_, _, code, err := runCmd("systemctl", args...)
	if err != nil {
		return false, fmt.Errorf("systemctl is-enabled %s: %w", name, err)
	}
	return code == 0, nil
}

// Run executes systemctl with args and fails on a non-zero exit or when the
// command could not be started.
func Run(args ...string) error {
	stdout, stderr, code, err := runCmd("systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %v: %w", args, err)
	}
	if code != 0 {
		return fmt.Errorf("systemctl %v failed (exit %d): %s%s", args, code, stdout, stderr)
	}
	return nil
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
