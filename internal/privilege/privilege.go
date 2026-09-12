package privilege

import (
	"fmt"
	"os"
	"strings"
)

// Mode selects how privileged gonf apply invocations are wrapped.
type Mode int

const (
	// None never wraps gonf (SSH as root / already privileged).
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

// WrapApplyCmd returns a remote/local shell command that runs gonf apply.
// applyArgs is typically "apply -" or "apply -n -" (no leading gonf).
// When elevate is false, returns "gonf "+applyArgs.
func WrapApplyCmd(mode Mode, elevate bool, applyArgs string) (string, error) {
	cmd := "gonf " + strings.TrimSpace(applyArgs)
	if !elevate {
		return cmd, nil
	}
	switch mode {
	case None:
		if os.Geteuid() == 0 {
			return cmd, nil
		}
		return "", fmt.Errorf("privilege: privileged apply requires sudo or doas (host WithPrivilege), or run as root")
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
		if os.Geteuid() == 0 {
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
