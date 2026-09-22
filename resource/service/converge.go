package service

import (
	"fmt"

	"github.com/snonux/gonf/resource"
)

// applyWith converges s through backend b. This is the one copy of the
// service policy shared by every OS backend: reject WithUser where the
// backend has no per-user manager, probe running/enabled, derive the action
// list, honour the change gate and dry-run, run the actions in order, and
// note the result. Tests call it directly with a fake backend, so no detector
// or runner global has to be patched to exercise the policy.
//
// The WithUser refusal reason comes from the backend (userSupport), so this
// policy carries no knowledge of which manager offers per-user services.
func (s *Service) applyWith(b backend) error {
	if s.user {
		if err := b.userSupport(); err != nil {
			return fmt.Errorf("service[%s]: %w", s.name, err)
		}
	}
	u := unit{name: s.name, user: s.user}
	running, err := b.running(u)
	if err != nil {
		return err
	}
	enabled, err := b.enabled(u)
	if err != nil {
		return err
	}

	id := resource.FormatID("Service", s.name)
	verbs, held := s.actions(id, running, enabled)
	return runActions(id, b, u, verbs, held)
}

// actions returns the ordered verbs that move s from the probed state to
// its desired state. Absent stops before disabling; present enables before
// starting. A running present service gets its restart/reload (reload wins
// when both are set) unless the change gate holds it (embed.ChangeGate.Holds:
// armed by OnChange and no watched resource changed this apply), which is
// reported via held. State convergence (enable/start/stop/disable) is never
// gated — only the once-per-change action is.
func (s *Service) actions(id string, running, enabled bool) (verbs []verb, held bool) {
	if s.Absent {
		if running {
			verbs = append(verbs, verbStop)
		}
		if enabled {
			verbs = append(verbs, verbDisable)
		}
		return verbs, false
	}
	if !enabled {
		verbs = append(verbs, verbEnable)
	}
	switch {
	case !running:
		verbs = append(verbs, verbStart)
	case !s.reload && !s.restart:
		// Running and no restart/reload requested: nothing more to do.
	case s.Holds(resource.AnyChanged):
		s.LogHeld(id, "restart/reload")
		held = true
	case s.reload:
		verbs = append(verbs, verbReload)
	default:
		verbs = append(verbs, verbRestart)
	}
	return verbs, held
}

// runActions performs verbs through b in order (or only logs them in a dry
// run) and notes the result, via the shared runner resource.Converge that
// Timer uses too, so log wording and result reporting live in one place for
// every backend. With no verbs the service is idle: skipped when held (the
// gate held a requested restart/reload), ok when already converged. The
// first failing action aborts the rest and is returned; nothing is noted in
// that case.
func runActions(id string, b backend, u unit, verbs []verb, held bool) error {
	actions := make([]resource.Action, len(verbs))
	for i, v := range verbs {
		actions[i] = backendAction{b: b, u: u, v: v}
	}
	return resource.Converge(id, actions, held)
}

// actionLogLines renders the complete log lines for v on b: the dry-run line
// and the line logged after the action succeeded, exactly as runActions
// logs them. Kept separate so tests can pin every backend's
// operator-visible wording.
func actionLogLines(b backend, u unit, v verb) (would, did string) {
	return resource.LogLines(backendAction{b: b, u: u, v: v})
}
