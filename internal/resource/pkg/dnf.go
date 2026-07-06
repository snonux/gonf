package pkg

import (
	"fmt"
	"log"

	"codeberg.org/snonux/gonf/internal/exec"
)

func applyDNF(name string, ensure Ensure) error {
	var args []string

	switch ensure {
	case PkgPresent:
		args = []string{"install", "-y", name}
	case PkgAbsent:
		args = []string{"remove", "-y", name}
	case PkgLatest:
		args = []string{"install", "-y", name}
	default:
		log.Fatalf("unsupported ensure state: %v", ensure)
	}

	stdout, stderr, exitCode, err := exec.Run("dnf", args...)
	if err != nil {
		return fmt.Errorf("failed to execute dnf: %w", err)
	}

	if exitCode != 0 {
		return fmt.Errorf("dnf failed with exit code %d: %s\n%s", exitCode, stdout, stderr)
	}

	return nil
}
