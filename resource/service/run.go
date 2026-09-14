package service

import "github.com/snonux/gonf/internal/exec"

// runCmd executes an external command. Swapped in unit tests.
var runCmd = exec.Run

// SetRunCmdForTest swaps the service manager command runner (tests only).
func SetRunCmdForTest(run func(name string, args ...string) (string, string, int, error)) {
	runCmd = run
}

// ResetRunCmdForTest restores the real command runner.
func ResetRunCmdForTest() {
	runCmd = exec.Run
}
