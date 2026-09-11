package service

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

const netbsdService = "/usr/sbin/service"

func applyNetBSD(s *Service) error {
	id := fmt.Sprintf("Service[%s]", s.name)

	running, err := netbsdRunning(s.name)
	if err != nil {
		return err
	}
	enabled, err := netbsdEnabled(s.name)
	if err != nil {
		return err
	}

	type action struct {
		desc string
		run  func() error
	}
	var actions []action

	if s.Absent {
		if running {
			actions = append(actions, action{"service " + s.name + " stop", func() error {
				return netbsdSvcRun(s.name, "stop")
			}})
		}
		if enabled {
			actions = append(actions, action{"disable " + s.name, func() error {
				return netbsdSetEnabled(s.name, false)
			}})
		}
	} else {
		if !enabled {
			actions = append(actions, action{"enable " + s.name, func() error {
				return netbsdSetEnabled(s.name, true)
			}})
		}
		if !running {
			actions = append(actions, action{"service " + s.name + " start", func() error {
				return netbsdSvcRun(s.name, "start")
			}})
		} else if s.reload {
			actions = append(actions, action{"service " + s.name + " reload", func() error {
				return netbsdSvcRun(s.name, "reload")
			}})
		} else if s.restart {
			actions = append(actions, action{"service " + s.name + " restart", func() error {
				return netbsdSvcRun(s.name, "restart")
			}})
		}
	}

	if len(actions) == 0 {
		noteResult(id, false)
		return nil
	}

	if resource.DryRun() {
		for _, a := range actions {
			logger.Info("dry-run: would %s", a.desc)
		}
		noteResult(id, true)
		return nil
	}

	for _, a := range actions {
		if err := a.run(); err != nil {
			return err
		}
		logger.Info("%s", a.desc)
	}
	noteResult(id, true)
	return nil
}

func netbsdRunning(name string) (bool, error) {
	_, _, code, err := runCmd(netbsdService, name, "status")
	if err != nil {
		return false, fmt.Errorf("service %s status: %w", name, err)
	}
	return code == 0, nil
}

func netbsdEnabled(name string) (bool, error) {
	_, _, code, err := runCmd(netbsdService, "-e", name)
	if err != nil {
		return false, fmt.Errorf("service -e %s: %w", name, err)
	}
	return code == 0, nil
}

func netbsdSvcRun(name, action string) error {
	stdout, stderr, code, err := runCmd(netbsdService, name, action)
	if err != nil {
		return fmt.Errorf("service %s %s: %w", name, action, err)
	}
	if code != 0 {
		return fmt.Errorf("service %s %s failed (exit %d): %s%s", name, action, code, stdout, stderr)
	}
	return nil
}

// netbsdSetEnabled writes /etc/rc.conf.d/NAME with NAME=YES|NO (overrides rc.conf).
func netbsdSetEnabled(name string, enabled bool) error {
	dir := "/etc/rc.conf.d"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	val := "NO"
	if enabled {
		val = "YES"
	}
	content := name + "=" + val + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
