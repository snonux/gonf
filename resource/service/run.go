package service

import (
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource/systemd"
)

// runCmd executes an external command for the BSD backends (rcctl and
// service(8)). Swapped in unit tests. The systemctl backend does not use it:
// it routes through the shared runner in resource/systemd, which
// SetRunCmdForTest swaps alongside this one.
var runCmd = exec.Run

// SetRunCmdForTest swaps the service manager command runner (tests only).
// Both the BSD backend runner and the shared systemd runner are swapped so
// systemd-backed services follow the fake too.
func SetRunCmdForTest(run func(name string, args ...string) (string, string, int, error)) {
	runCmd = run
	systemd.SetRunCmdForTest(run)
}

// ResetRunCmdForTest restores the real command runners.
func ResetRunCmdForTest() {
	runCmd = exec.Run
	systemd.ResetRunCmdForTest()
}
