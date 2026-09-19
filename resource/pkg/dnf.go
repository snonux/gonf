package pkg

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func applyDNF(p *Package) error {
	id := fmt.Sprintf("Package[%s]", p.name)
	installed, err := dnfInstalled(p)
	if err != nil {
		return err
	}

	var args []string
	switch {
	case p.Absent:
		if !installed {
			resource.NoteResult(id, false)
			return nil
		}
		args = []string{"remove", "-y", p.name}
	case p.latest:
		// Mirror the BSD backends: "latest" always acts, because probing
		// whether a package is up to date would need a slow, parse-heavy
		// dnf check-update. dnf update of an up-to-date package is a
		// no-op but is still reported as a change, like pkg upgrade on
		// FreeBSD.
		args = []string{"update", "-y", p.name}
	case installed:
		resource.NoteResult(id, false)
		return nil
	default:
		args = []string{"install", "-y", p.name}
	}

	if resource.DryRun() {
		logger.Info("dry-run: would run dnf %v", args)
		resource.NoteResult(id, true)
		return nil
	}

	stdout, stderr, exitCode, err := p.run("dnf", args...)
	if err != nil {
		return fmt.Errorf("failed to execute dnf: %w", err)
	}

	if exitCode != 0 {
		return fmt.Errorf("dnf failed with exit code %d: %s\n%s", exitCode, stdout, stderr)
	}

	logger.Info("dnf %v completed", args)
	resource.NoteResult(id, true)
	return nil
}

func dnfInstalled(p *Package) (bool, error) {
	// rpm -q only consults the local rpm database: unlike
	// `dnf list installed` it never triggers a repository metadata
	// refresh, so the probe stays cheap. A non-zero exit code means
	// the package is not installed; a start failure is an error.
	_, _, code, err := p.run("rpm", "-q", p.name)
	if err != nil {
		return false, fmt.Errorf("rpm -q %s: %w", p.name, err)
	}
	return code == 0, nil
}
