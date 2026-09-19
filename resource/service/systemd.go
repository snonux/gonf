package service

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// applySystemd converges s via systemctl. Unlike Timer, Service performs no
// unit-name validation here — the name is passed to systemctl as given. That
// drift is deliberate for now: validation stays with the callers that had it,
// and the shared mechanics live in resource/systemd.
func applySystemd(s *Service) error {
	id := fmt.Sprintf("Service[%s]", s.name)

	running, err := systemd.IsActive(s.name, s.user)
	if err != nil {
		return err
	}
	enabled, err := systemd.IsEnabled(s.name, s.user)
	if err != nil {
		return err
	}

	var actions [][]string
	held := false // change gate suppressed the restart/reload action
	if s.Absent {
		if running {
			actions = append(actions, systemd.Args(s.user, "stop", s.name))
		}
		if enabled {
			actions = append(actions, systemd.Args(s.user, "disable", s.name))
		}
	} else {
		if !enabled {
			actions = append(actions, systemd.Args(s.user, "enable", s.name))
		}
		if !running {
			actions = append(actions, systemd.Args(s.user, "start", s.name))
		} else if s.reload || s.restart {
			// The gated action only fires after a watched resource changed.
			if s.gateHolds() {
				logger.Debug("%s: restart/reload held by change gate (no watched dependency changed)", id)
				held = true
			} else if s.reload {
				actions = append(actions, systemd.Args(s.user, "reload", s.name))
			} else {
				actions = append(actions, systemd.Args(s.user, "restart", s.name))
			}
		}
	}

	if len(actions) == 0 {
		if held {
			resource.Note(id, resource.StatusSkipped)
			return nil
		}
		resource.NoteResult(id, false)
		return nil
	}

	if resource.DryRun() {
		for _, a := range actions {
			logger.Info("dry-run: would run systemctl %v", a)
		}
		resource.NoteResult(id, true)
		return nil
	}

	for _, a := range actions {
		if err := systemd.Run(a...); err != nil {
			return err
		}
		logger.Info("systemctl %v", a)
	}
	resource.NoteResult(id, true)
	return nil
}
