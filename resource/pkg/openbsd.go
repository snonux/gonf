package pkg

import (
	"fmt"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func applyOpenBSD(p *Package) error {
	id := fmt.Sprintf("Package[%s]", p.name)
	installed, err := openbsdInstalled(p)
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
		args = []string{p.name}
		if resource.DryRun() {
			logger.Info("dry-run: would run pkg_delete %v", args)
			resource.NoteResult(id, true)
			return nil
		}
		if err := runOrErr(p, "pkg_delete", args...); err != nil {
			return err
		}
		logger.Info("pkg_delete %v", args)
		resource.NoteResult(id, true)
		return nil
	case p.latest && installed:
		args = []string{"-u", p.name}
	case installed:
		resource.NoteResult(id, false)
		return nil
	default:
		args = []string{p.name}
	}

	if resource.DryRun() {
		logger.Info("dry-run: would run pkg_add %v", args)
		resource.NoteResult(id, true)
		return nil
	}
	if err := runOrErr(p, "pkg_add", args...); err != nil {
		return err
	}
	logger.Info("pkg_add %v", args)
	resource.NoteResult(id, true)
	return nil
}

func openbsdInstalled(p *Package) (bool, error) {
	// pkgspec stem-* matches any version of the package.
	_, _, code, err := p.run("pkg_info", "-e", p.name+"-*")
	if err != nil {
		return false, fmt.Errorf("pkg_info -e %s-*: %w", p.name, err)
	}
	return code == 0, nil
}
