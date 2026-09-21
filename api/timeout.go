package api

import (
	"time"

	"github.com/snonux/gonf/internal/exec"
)

// SetCommandTimeout overrides the process-wide default timeout applied to
// every backend command execution (package manager, systemctl, crontab,
// rcctl, ...) that goes through internal/exec, and to File WithValidation
// validators, which read the same default — the CLI wires this to
// "-cmd-timeout" (see internal/cli/cli.go). d <= 0 is ignored, mirroring
// exec.SetDefaultTimeout: there is no "unlimited" process-wide default, only
// a per-call opt-out (internal/exec's Opts.Timeout < 0) for the rare caller
// that genuinely needs one.
func SetCommandTimeout(d time.Duration) {
	exec.SetDefaultTimeout(d)
}

// CommandTimeout returns the currently active process-wide default command
// timeout.
func CommandTimeout() time.Duration {
	return exec.DefaultTimeout()
}
