package pkg

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func applyOpenBSD(p *Package) error {
	id := fmt.Sprintf("Package[%s]", p.name)
	installed, err := openbsdInstalled(p.name)
	if err != nil {
		return err
	}

	var args []string
	switch {
	case p.Absent:
		if !installed {
			notePkg(id, false)
			return nil
		}
		args = []string{p.name}
		if resource.DryRun() {
			logger.Info("dry-run: would run pkg_delete %v", args)
			notePkg(id, true)
			return nil
		}
		if err := runOrErr("pkg_delete", args...); err != nil {
			return err
		}
		logger.Info("pkg_delete %v", args)
		notePkg(id, true)
		return nil
	case p.latest:
		args = []string{"-u", p.name}
	case installed:
		notePkg(id, false)
		return nil
	default:
		args = []string{p.name}
	}

	if resource.DryRun() {
		logger.Info("dry-run: would run pkg_add %v", args)
		notePkg(id, true)
		return nil
	}
	if err := runOrErr("pkg_add", args...); err != nil {
		return err
	}
	logger.Info("pkg_add %v", args)
	notePkg(id, true)
	return nil
}

func openbsdInstalled(name string) (bool, error) {
	// pkgspec stem-* matches any version of the package.
	_, _, code, err := runCmd("pkg_info", "-e", name+"-*")
	if err != nil {
		return false, fmt.Errorf("pkg_info -e %s-*: %w", name, err)
	}
	return code == 0, nil
}
