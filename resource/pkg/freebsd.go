package pkg

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func applyFreeBSDPkg(p *Package) error {
	id := fmt.Sprintf("Package[%s]", p.name)
	installed, err := freebsdPkgInstalled(p)
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
		args = []string{"upgrade", "-y", p.name}
	case installed:
		resource.NoteResult(id, false)
		return nil
	default:
		args = []string{"install", "-y", p.name}
	}

	if resource.DryRun() {
		logger.Info("dry-run: would run pkg %v", args)
		resource.NoteResult(id, true)
		return nil
	}
	if err := runOrErr(p, "pkg", args...); err != nil {
		return err
	}
	logger.Info("pkg %v", args)
	resource.NoteResult(id, true)
	return nil
}

func freebsdPkgInstalled(p *Package) (bool, error) {
	_, _, code, err := p.run("pkg", "info", "-e", p.name)
	if err != nil {
		return false, fmt.Errorf("pkg info -e %s: %w", p.name, err)
	}
	return code == 0, nil
}
