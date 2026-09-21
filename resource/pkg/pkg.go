// Package pkg implements the package resource with per-OS backends.
package pkg

import (
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// runCmd is swapped in unit tests for packages without an environment.
// runCmdWithEnv receives a complete, inherited environment for packages with
// WithEnv; keeping the seams separate preserves the legacy unset behavior.
var (
	runCmd        = exec.Run
	runCmdWithEnv = func(env []string, name string, args ...string) (string, string, int, error) {
		return exec.RunWith(exec.Opts{Env: env}, name, args...)
	}
)

// detectPkgManager names the host's package manager; selectBackend maps the
// name to a backend. It is swapped by SetDetectPackageManagerForTest (CI
// runners are often Ubuntu); in-package tests instead hand a backend to
// applyWith directly.
var detectPkgManager = detectPackageManager

// resource.Register takes this value as a resource.Applier. The assertion
// pins that contract at the declaration, so a renamed or re-signed Apply is
// reported here rather than at the Register call.
var _ resource.Applier = (*Package)(nil)

// Package reconciles an OS package's presence or absence using the
// platform package manager (dnf on Linux, pkg on FreeBSD, pkgin on
// NetBSD, pkg_add on OpenBSD).
type Package struct {
	embed.DependsOn
	embed.Absence
	name   string
	latest bool
	env    map[string]string
}

// SetLatest upgrades the package to the newest available version instead of
// only ensuring it is installed.
func (p *Package) SetLatest() { p.latest = true }

// SetEnv configures extra environment variables for package-manager probes
// and mutations. It copies the caller's map (nil stays nil) so a recipe that
// mutates or reuses the map after WithEnv cannot alter the registered
// resource, its plan draft or its recorded plan op. Cmd.SetEnv follows the
// same contract, since both are reached through the one shared WithEnv option.
func (p *Package) SetEnv(env map[string]string) {
	p.env = maps.Clone(env)
}

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

// SetRunCmdWithEnvForTest swaps the runner used when WithEnv is configured.
// The environment is complete: it includes the inherited process environment
// with the resource's values overlaid.
func SetRunCmdWithEnvForTest(run func(env []string, name string, args ...string) (string, string, int, error)) {
	runCmdWithEnv = run
}

// ResetRunCmdWithEnvForTest restores the environment-aware runner.
func ResetRunCmdWithEnvForTest() {
	runCmdWithEnv = func(env []string, name string, args ...string) (string, string, int, error) {
		return exec.RunWith(exec.Opts{Env: env}, name, args...)
	}
}

// Apply runs the package reconciliation directly for the legacy resource path.
func (p *Package) Apply() error { return p.apply() }

// Present registers a package resource ensuring name is installed; IsLatest
// upgrades it to the newest available version.
func Present(name string, opts ...opt.PackageOption) resource.Resource {
	p := &Package{
		name: name,
	}

	for _, o := range opts {
		o.Apply(p)
	}

	r := resource.Register("Package", p.name, p, p.DependsOn.IDs...)
	resource.RecordPlanDraft(p.planDraft(r.ID()))
	return r
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

// SetDetectPackageManagerForTest stubs OS package-manager detection (tests only).
func SetDetectPackageManagerForTest(fn func() (string, error)) {
	detectPkgManager = fn
}

// ResetDetectPackageManagerForTest restores the real detector after a test stub.
func ResetDetectPackageManagerForTest() {
	detectPkgManager = detectPackageManager
}

// apply selects the host's backend and converges p through it with p's own
// runner (which carries WithEnv). The shared policy lives in applyWith.
func (p *Package) apply() error {
	b, err := selectBackend()
	if err != nil {
		return err
	}
	return p.applyWith(b, p.run)
}

// planDraft records p as a "package" plan draft under id.
func (p *Package) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:   "package",
		ID:     id,
		Name:   p.name,
		Absent: p.Absent,
		Latest: p.latest,
		Deps:   p.DependsOn.SortedIDs(),
	}
	// The draft gets its own copy: stored drafts outlive this Package and
	// must not share mutable state with it. maps.Clone keeps nil as nil.
	d.Env = maps.Clone(p.env)
	return d
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

// run is p's runner: the legacy seam when no WithEnv is set, otherwise the
// environment-aware seam with p's variables overlaid on the inherited
// environment. It satisfies the runner type the backends are handed.
func (p *Package) run(bin string, args ...string) (string, string, int, error) {
	if p.env == nil {
		return runCmd(bin, args...)
	}
	return runCmdWithEnv(exec.MergeEnv(p.env), bin, args...)
}
