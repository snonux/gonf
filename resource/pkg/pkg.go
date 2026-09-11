// Package pkg implements the package resource with per-OS backends (dnf).
package pkg

import (
	"errors"
	"os"

	opt "github.com/snonux/gonf/api/options"
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

func (p *Package) apply() error {
	pkgMan, err := detectPackageManager()
	if err != nil {
		return err
	}

	switch pkgMan {
	case "dnf":
		return applyDNF(p)
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
	switch {
	case exists("/etc/fedora-release"):
		fallthrough
	case exists("/etc/centos-release"):
		fallthrough
	case exists("/etc/redhat-release"):
		fallthrough
	case exists("/etc/rocky-release"):
		return "dnf", nil
	}
	return "", errors.New("unable to detect package manager!")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
