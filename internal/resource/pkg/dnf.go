package pkg

import (
	"fmt"

	"codeberg.org/snonux/gonf/internal/exec"
)

func applyDNF(p *Package) error {
	var args []string

	if p.Absent {
		args = []string{"remove", "-y", p.name}
	} else if p.latest {
		// update ensures the package is installed and updated to the latest version.
		args = []string{"update", "-y", p.name}
	} else {
		// install ensures the package is installed, but does not update it if already present.
		args = []string{"install", "-y", p.name}
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
