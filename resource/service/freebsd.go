package service

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func applyFreeBSD(s *Service) error {
	id := fmt.Sprintf("Service[%s]", s.name)

	running, err := freebsdStatus(s.name)
	if err != nil {
		return err
	}
	enabled, err := freebsdEnabled(s.name)
	if err != nil {
		return err
	}

	var actions []struct {
		bin  string
		args []string
	}
	held := false // change gate suppressed the restart/reload action
	add := func(bin string, args ...string) {
		actions = append(actions, struct {
			bin  string
			args []string
		}{bin, args})
	}

	if s.Absent {
		if running {
			add("service", s.name, "stop")
		}
		if enabled {
			add("service", s.name, "disable")
		}
	} else {
		if !enabled {
			add("service", s.name, "enable")
		}
		if !running {
			add("service", s.name, "start")
		} else if s.reload || s.restart {
			// The gated action only fires after a watched resource changed.
			if s.gateHolds() {
				logger.Debug("%s: restart/reload held by change gate (no watched dependency changed)", id)
				held = true
			} else if s.reload {
				add("service", s.name, "reload")
			} else {
				add("service", s.name, "restart")
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
			logger.Info("dry-run: would run %s %v", a.bin, a.args)
		}
		resource.NoteResult(id, true)
		return nil
	}

	for _, a := range actions {
		if err := freebsdRun(a.bin, a.args...); err != nil {
			return err
		}
		logger.Info("%s %v", a.bin, a.args)
	}
	resource.NoteResult(id, true)
	return nil
}

func freebsdStatus(name string) (bool, error) {
	_, _, code, err := runCmd("service", name, "status")
	if err != nil {
		return false, fmt.Errorf("service %s status: %w", name, err)
	}
	return code == 0, nil
}

func freebsdEnabled(name string) (bool, error) {
	// service NAME enabled exits 0 when enabled in rc.conf.
	_, _, code, err := runCmd("service", name, "enabled")
	if err != nil {
		return false, fmt.Errorf("service %s enabled: %w", name, err)
	}
	return code == 0, nil
}

func freebsdRun(bin string, args ...string) error {
	stdout, stderr, code, err := runCmd(bin, args...)
	if err != nil {
		return fmt.Errorf("%s %v: %w", bin, args, err)
	}
	if code != 0 {
		return fmt.Errorf("%s %v failed (exit %d): %s%s", bin, args, code, stdout, stderr)
	}
	return nil
}
