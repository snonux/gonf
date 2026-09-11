// Package pkg implements the package resource with per-OS backends.
package pkg

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
)

type Package struct {
	embed.DependsOn
	embed.Absence
	name   string
	latest bool
}

func (p *Package) SetLatest() { p.latest = true }

// runCmd is swapped in unit tests.
var runCmd = exec.Run

func (p *Package) apply() error {
	pkgMan, err := detectPackageManager()
	if err != nil {
		return err
	}

	switch pkgMan {
	case "dnf":
		return applyDNF(p)
	case "openbsd":
		return applyOpenBSD(p)
	case "freebsd":
		return applyFreeBSDPkg(p)
	case "netbsd":
		return applyNetBSD(p)
	}

	return errors.New("unsupported package manager")
}

func Present(name string, opts ...opt.Option) resource.Resource {
	p := &Package{
		name: name,
	}

	for _, o := range opts {
		o(p)
	}

	return resource.Register("Package", p.name,
		resource.ApplierFunc(func() error { return p.apply() }), p.DependsOn.IDs...)
}

func Absent(name string, opts ...opt.Option) resource.Resource {
	opts = append(opts, opt.IsAbsent)
	return Present(name, opts...)
}

func detectPackageManager() (string, error) {
	switch runtime.GOOS {
	case "openbsd":
		return "openbsd", nil
	case "freebsd":
		return "freebsd", nil
	case "netbsd":
		return "netbsd", nil
	case "linux":
		switch {
		case exists("/etc/fedora-release"),
			exists("/etc/centos-release"),
			exists("/etc/redhat-release"),
			exists("/etc/rocky-release"):
			return "dnf", nil
		}
		return "", errors.New("unable to detect package manager on linux")
	default:
		return "", fmt.Errorf("unable to detect package manager on %s", runtime.GOOS)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func notePkg(id string, changed bool) {
	if resource.DryRun() {
		if changed {
			resource.Note(id, resource.StatusWouldChange)
		} else {
			resource.Note(id, resource.StatusOK)
		}
		return
	}
	if changed {
		resource.Note(id, resource.StatusChanged)
	} else {
		resource.Note(id, resource.StatusOK)
	}
}

func runOrErr(bin string, args ...string) error {
	stdout, stderr, code, err := runCmd(bin, args...)
	if err != nil {
		return fmt.Errorf("%s %v: %w", bin, args, err)
	}
	if code != 0 {
		return fmt.Errorf("%s %v failed (exit %d): %s%s", bin, args, code, stdout, stderr)
	}
	return nil
}
