package service

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func applyRcctl(s *Service) error {
	id := fmt.Sprintf("Service[%s]", s.name)

	running, err := rcctlCheck(s.name)
	if err != nil {
		return err
	}
	enabled, err := rcctlEnabled(s.name)
	if err != nil {
		return err
	}

	var actions [][]string
	held := false // change gate suppressed the restart/reload action
	if s.Absent {
		if running {
			actions = append(actions, []string{"stop", s.name})
		}
		if enabled {
			actions = append(actions, []string{"disable", s.name})
		}
	} else {
		if !enabled {
			actions = append(actions, []string{"enable", s.name})
		}
		if !running {
			actions = append(actions, []string{"start", s.name})
		} else if s.reload || s.restart {
			// The gated action only fires after a watched resource changed.
			if s.gateHolds() {
				logger.Debug("%s: restart/reload held by change gate (no watched dependency changed)", id)
				held = true
			} else if s.reload {
				actions = append(actions, []string{"reload", s.name})
			} else {
				actions = append(actions, []string{"restart", s.name})
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
			logger.Info("dry-run: would run rcctl %v", a)
		}
		resource.NoteResult(id, true)
		return nil
	}

	for _, a := range actions {
		if err := rcctlRun(a...); err != nil {
			return err
		}
		logger.Info("rcctl %v", a)
	}
	resource.NoteResult(id, true)
	return nil
}

func rcctlCheck(name string) (bool, error) {
	_, _, code, err := runCmd("rcctl", "check", name)
	if err != nil {
		return false, fmt.Errorf("rcctl check %s: %w", name, err)
	}
	return code == 0, nil
}

func rcctlEnabled(name string) (bool, error) {
	// Package daemons often print nothing; exit 0 means enabled (status on).
	_, _, code, err := runCmd("rcctl", "get", name, "status")
	if err != nil {
		return false, fmt.Errorf("rcctl get %s status: %w", name, err)
	}
	return code == 0, nil
}

func rcctlRun(args ...string) error {
	stdout, stderr, code, err := runCmd("rcctl", args...)
	if err != nil {
		return fmt.Errorf("rcctl %v: %w", args, err)
	}
	if code != 0 {
		return fmt.Errorf("rcctl %v failed (exit %d): %s%s", args, code, stdout, stderr)
	}
	return nil
}
