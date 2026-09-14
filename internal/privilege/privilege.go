package privilege

import (
	"fmt"
	"os"
	"strings"
)

// Mode selects how privileged gonf apply invocations are wrapped.
type Mode int

const (
	// None never wraps gonf. Local apply runs unwrapped only when this
	// process is root; remote push rejects elevated ops with None entirely
	// (see WrapApplyCmd): the remote login's privilege is not knowable here.
	None Mode = iota
	Sudo
	Doas
)

// ParseMode accepts none|sudo|doas (case-insensitive).
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return None, nil
	case "sudo":
		return Sudo, nil
	case "doas":
		return Doas, nil
	default:
		return None, fmt.Errorf("privilege: unknown mode %q (want none|sudo|doas)", s)
	}
}

func (m Mode) String() string {
	switch m {
	case Sudo:
		return "sudo"
	case Doas:
		return "doas"
	default:
		return "none"
	}
}

// geteuid is swappable in tests so the euid-dependent LOCAL branch and the
// euid-free REMOTE doctrine can be asserted on any machine.
var geteuid = os.Geteuid

// WrapApplyCmd returns the remote shell command that runs gonf apply on an
// SSH target (built by push/fleet). applyArgs is typically "apply -" or
// "apply -n -" (no leading gonf). When elevate is false, returns
// "gonf "+applyArgs.
//
// Unlike WrapArgv (local re-exec), this decision must not depend on the
// controller's euid: mode None plus an elevated chunk is always an error,
// whether or not gonf itself runs as root. Point users at sudo/doas, or at
// dropping Privileged() when the SSH login is already root.
func WrapApplyCmd(mode Mode, elevate bool, applyArgs string) (string, error) {
	cmd := "gonf " + strings.TrimSpace(applyArgs)
	if !elevate {
		return cmd, nil
	}
	switch mode {
	case None:
		return "", fmt.Errorf("privilege: privileged chunk with -privilege=none: set -privilege sudo|doas (host WithPrivilege), or drop Privileged() when the SSH login is root")
	case Sudo:
		return "sudo -n " + cmd, nil
	case Doas:
		return "doas " + cmd, nil
	default:
		return "", fmt.Errorf("privilege: invalid mode %d", mode)
	}
}

// WrapArgv prefixes argv with sudo -n / doas when elevate is set.
func WrapArgv(mode Mode, elevate bool, argv []string) ([]string, error) {
	if !elevate {
		return argv, nil
	}
	switch mode {
	case None:
		// Local re-exec: this process's own euid is the correct authority.
		if geteuid() == 0 {
			return argv, nil
		}
		return nil, fmt.Errorf("privilege: privileged apply requires sudo or doas (host WithPrivilege), or run as root")
	case Sudo:
		return append([]string{"sudo", "-n"}, argv...), nil
	case Doas:
		return append([]string{"doas"}, argv...), nil
	default:
		return nil, fmt.Errorf("privilege: invalid mode %d", mode)
	}
}
