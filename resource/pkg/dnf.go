package pkg

import (
	"fmt"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func applyDNF(p *Package) error {
	id := fmt.Sprintf("Package[%s]", p.name)
	var args []string

	if p.Absent {
		args = []string{"remove", "-y", p.name}
	} else if p.latest {
		args = []string{"update", "-y", p.name}
	} else {
		args = []string{"install", "-y", p.name}
	}

	if resource.DryRun() {
		resource.Note(id, resource.StatusWouldChange)
		logger.Info("dry-run: would run dnf %v", args)
		return nil
	}

	stdout, stderr, exitCode, err := exec.Run("dnf", args...)
	if err != nil {
		return fmt.Errorf("failed to execute dnf: %w", err)
	}

	if exitCode != 0 {
		return fmt.Errorf("dnf failed with exit code %d: %s\n%s", exitCode, stdout, stderr)
	}

	resource.Note(id, resource.StatusChanged)
	logger.Info("dnf %v completed", args)
	return nil
}
