package pkg

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

// applyWith converges p through backend b, running every command via run.
// This is the one copy of the package policy shared by all OS backends:
// probe, pick the transition the desired state needs, honour dry-run, run
// it, and note the result. Tests call it directly with a fake backend or a
// fake runner, so no package-level seam has to be patched to exercise it.
func (p *Package) applyWith(b backend, run runner) error {
	id := fmt.Sprintf("Package[%s]", p.name)
	installed, err := b.installed(run, p.name)
	if err != nil {
		return err
	}

	c, act := p.transition(b, installed)
	if !act {
		resource.NoteResult(id, false)
		return nil
	}

	would, did := c.logLines()
	if resource.DryRun() {
		logger.Info("%s", would)
		resource.NoteResult(id, true)
		return nil
	}
	if err := b.execute(run, c); err != nil {
		return err
	}
	logger.Info("%s", did)
	resource.NoteResult(id, true)
	return nil
}

// logLines renders how c is logged: the dry-run line and the line logged
// after it ran (plus the backend's doneSuffix, so dnf keeps its historical
// "dnf [args] completed"). The wording is unchanged from before the backend
// refactor; tests pin it per backend.
func (c command) logLines() (would, did string) {
	return fmt.Sprintf("dry-run: would run %s %v", c.label, c.args),
		fmt.Sprintf("%s %v%s", c.label, c.args, c.doneSuffix)
}

// transition returns the command that moves p from the probed state to its
// desired state, or act=false when p has already converged.
//
// IsLatest always acts on every backend: probing whether a package is up to
// date would need a slow, parse-heavy query per manager (e.g. dnf
// check-update), so an upgrade of an up-to-date package runs and is reported
// as a change. Absent takes precedence over IsLatest.
func (p *Package) transition(b backend, installed bool) (c command, act bool) {
	switch {
	case p.Absent && !installed:
		return command{}, false
	case p.Absent:
		return b.removeCmd(p.name), true
	case p.latest:
		return b.upgradeCmd(p.name, installed), true
	case installed:
		return command{}, false
	default:
		return b.installCmd(p.name), true
	}
}
