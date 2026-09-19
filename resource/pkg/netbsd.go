package pkg

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

const (
	netbsdPkgin   = "/usr/pkg/bin/pkgin"
	netbsdPkgInfo = "/usr/sbin/pkg_info"
)

func applyNetBSD(p *Package) error {
	id := fmt.Sprintf("Package[%s]", p.name)
	installed, err := netbsdInstalled(p)
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
		args = []string{"-y", "remove", p.name}
	case p.latest:
		args = []string{"-y", "install", p.name} // pkgin install upgrades when newer available
	case installed:
		resource.NoteResult(id, false)
		return nil
	default:
		args = []string{"-y", "install", p.name}
	}

	if resource.DryRun() {
		logger.Info("dry-run: would run pkgin %v", args)
		resource.NoteResult(id, true)
		return nil
	}
	if err := runOrErr(p, netbsdPkgin, args...); err != nil {
		return err
	}
	logger.Info("pkgin %v", args)
	resource.NoteResult(id, true)
	return nil
}

func netbsdInstalled(p *Package) (bool, error) {
	_, _, code, err := p.run(netbsdPkgInfo, "-e", p.name)
	if err != nil {
		return false, fmt.Errorf("pkg_info -e %s: %w", p.name, err)
	}
	return code == 0, nil
}
