package pkg

import (
	"fmt"

	"github.com/snonux/gonf/resource"
)

var _ resource.Action = execution{}

// execution is the command of p's transition as a resource.Action for the
// shared runner: the backend performs it with the Package's runner.
type execution struct {
	b   backend
	run runner
	c   command
}

// Do runs the command through its backend.
func (e execution) Do() error { return e.b.execute(e.run, e.c) }

// Describe returns how the command is logged: would ("run <label> [args]")
// follows the "dry-run: would " prefix in a dry run, did is logged after it
// ran (plus the backend's doneSuffix, so dnf keeps its historical
// "dnf [args] completed"). The wording is unchanged from before the backend
// refactor; tests pin it per backend via logLines.
func (e execution) Describe() (would, did string) {
	return fmt.Sprintf("run %s %v", e.c.label, e.c.args),
		fmt.Sprintf("%s %v%s", e.c.label, e.c.args, e.c.doneSuffix)
}

// applyWith converges p through backend b, running every command via run.
// This is the one copy of the package policy shared by all OS backends: it
// probes and picks the transition the desired state needs; dry-run handling,
// running the command and noting the result belong to the shared runner
// resource.Converge (the one Service and Timer use too), fed at most one
// execution. Tests call it directly with a fake backend or a fake runner, so
// no package-level seam has to be patched to exercise it.
func (p *Package) applyWith(b backend, run runner) error {
	id := resource.FormatID("Package", p.name)
	installed, err := b.installed(run, p.name)
	if err != nil {
		return err
	}

	var actions []resource.Action
	if c, act := p.transition(b, installed); act {
		actions = append(actions, execution{b: b, run: run, c: c})
	}
	// Packages have no change gate, so an idle package is noted ok, never
	// skipped (held=false).
	return resource.Converge(id, actions, false)
}

// logLines renders the complete log lines resource.Converge emits for c.
// execution.Describe reads only the command, so no backend or runner is
// needed here.
func (c command) logLines() (would, did string) {
	return resource.LogLines(execution{c: c})
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
