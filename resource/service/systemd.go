package service

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func applySystemd(s *Service) error {
	id := fmt.Sprintf("Service[%s]", s.name)

	running, err := systemdIsActive(s.name, s.user)
	if err != nil {
		return err
	}
	enabled, err := systemdIsEnabled(s.name, s.user)
	if err != nil {
		return err
	}

	var actions [][]string
	if s.Absent {
		if running {
			actions = append(actions, systemdArgs(s.user, "stop", s.name))
		}
		if enabled {
			actions = append(actions, systemdArgs(s.user, "disable", s.name))
		}
	} else {
		if !enabled {
			actions = append(actions, systemdArgs(s.user, "enable", s.name))
		}
		if !running {
			actions = append(actions, systemdArgs(s.user, "start", s.name))
		} else if s.reload {
			actions = append(actions, systemdArgs(s.user, "reload", s.name))
		} else if s.restart {
			actions = append(actions, systemdArgs(s.user, "restart", s.name))
		}
	}

	if len(actions) == 0 {
		noteResult(id, false)
		return nil
	}

	if resource.DryRun() {
		for _, a := range actions {
			logger.Info("dry-run: would run systemctl %v", a)
		}
		noteResult(id, true)
		return nil
	}

	for _, a := range actions {
		if err := systemdRun(a...); err != nil {
			return err
		}
		logger.Info("systemctl %v", a)
	}
	noteResult(id, true)
	return nil
}

func systemdArgs(user bool, args ...string) []string {
	if user {
		return append([]string{"--user"}, args...)
	}
	return args
}

func systemdIsActive(name string, user bool) (bool, error) {
	args := systemdArgs(user, "is-active", "--quiet", name)
	_, _, code, err := runCmd("systemctl", args...)
	if err != nil {
		return false, fmt.Errorf("systemctl is-active %s: %w", name, err)
	}
	return code == 0, nil
}

func systemdIsEnabled(name string, user bool) (bool, error) {
	args := systemdArgs(user, "is-enabled", "--quiet", name)
	_, _, code, err := runCmd("systemctl", args...)
	if err != nil {
		return false, fmt.Errorf("systemctl is-enabled %s: %w", name, err)
	}
	return code == 0, nil
}

func systemdRun(args ...string) error {
	stdout, stderr, code, err := runCmd("systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %v: %w", args, err)
	}
	if code != 0 {
		return fmt.Errorf("systemctl %v failed (exit %d): %s%s", args, code, stdout, stderr)
	}
	return nil
}
