// Package pkg implements the package resource with per-OS backends.
package pkg

import (
	"errors"
	"fmt"
	"runtime"
	"slices"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// Package reconciles an OS package's presence or absence using the
// platform package manager (dnf on Linux, pkg on FreeBSD, pkgin on
// NetBSD, pkg_add on OpenBSD).
type Package struct {
	embed.DependsOn
	embed.Absence
	name   string
	latest bool
}

func (p *Package) SetLatest() { p.latest = true }

// runCmd is swapped in unit tests.
var runCmd = exec.Run

// SetRunCmdForTest swaps the package-manager command runner (tests only).
// Cross-package apply tests (e.g. plan.Apply on a package op) reach the
// backend's dnf/pkg/pkg_add/pkgin invocations through this seam, mirroring
// resource/systemd's SetRunCmdForTest.
func SetRunCmdForTest(run func(name string, args ...string) (string, string, int, error)) {
	runCmd = run
}

// ResetRunCmdForTest restores the real command runner after a test stub.
func ResetRunCmdForTest() {
	runCmd = exec.Run
}

// detectPkgManager is swapped in unit tests (CI runners are often Ubuntu).
var detectPkgManager = detectPackageManager

func (p *Package) apply() error {
	pkgMan, err := detectPkgManager()
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

// Present registers a package resource ensuring name is installed; IsLatest
// upgrades it to the newest available version.
func Present(name string, opts ...opt.PackageOption) resource.Resource {
	p := &Package{
		name: name,
	}

	for _, o := range opts {
		o.Apply(p)
	}

	r := resource.Register("Package", p.name,
		resource.ApplierFunc(func() error { return p.apply() }), p.DependsOn.IDs...)
	resource.RecordPlanDraft(p.planDraft(r.ID()))
	return r
}

func (p *Package) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:   "package",
		ID:     id,
		Name:   p.name,
		Absent: p.Absent,
		Latest: p.latest,
		Deps:   p.DependsOn.SortedIDs(),
	}
}

// Ensure builds and applies a package resource without registering it or
// recording a plan draft.
func Ensure(name string, opts ...opt.PackageOption) error {
	p := &Package{name: name}
	for _, o := range opts {
		o.Apply(p)
	}
	return p.apply()
}

// Absent registers a package resource ensuring name is removed.
func Absent(name string, opts ...opt.PackageOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
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
		case resource.Exists("/etc/fedora-release"),
			resource.Exists("/etc/centos-release"),
			resource.Exists("/etc/redhat-release"),
			resource.Exists("/etc/rocky-release"):
			return "dnf", nil
		}
		return "", errors.New("unable to detect package manager on linux")
	default:
		return "", fmt.Errorf("unable to detect package manager on %s", runtime.GOOS)
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

// SetDetectPackageManagerForTest stubs OS package-manager detection (tests only).
func SetDetectPackageManagerForTest(fn func() (string, error)) {
	detectPkgManager = fn
}

// ResetDetectPackageManagerForTest restores the real detector after a test stub.
func ResetDetectPackageManagerForTest() {
	detectPkgManager = detectPackageManager
}
