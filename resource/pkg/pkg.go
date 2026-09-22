// Package pkg implements the package resource with per-OS backends.
package pkg

import (
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/testseam"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

// resource.Register takes this value as a resource.Applier. The assertion
// pins that contract at the declaration, so a renamed or re-signed Apply is
// reported here rather than at the Register call.
var (
	_ resource.Applier   = (*Package)(nil)
	_ opt.Sensitivable   = (*Package)(nil)
	_ opt.MisuseReporter = (*Package)(nil)
)

// Package reconciles an OS package's presence or absence using the
// platform package manager (dnf on Linux, pkg on FreeBSD, pkgin on
// NetBSD, pkg_add on OpenBSD).
//
// The Sensitivity embed backs WithSensitive: the package operation's
// environment (WithEnv, e.g. a PKG_PATH with credentials) holds secret
// material, so a failing package-manager command reports only the sizes of
// its output, not the output (run). Plan apply sets it from a sensitive
// package op.
type Package struct {
	embed.DependsOn
	embed.Absence
	embed.Sensitivity
	embed.Misuse
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

// Apply runs the package reconciliation directly. It makes the value Present
// registers a resource.Applier; since the repository apply was retired (task
// e72) nothing calls it through the repository, and the plan engine applies
// the kind through its plan handler instead.
func (p *Package) Apply() error { return p.apply() }

// Present registers a package resource ensuring name is installed; IsLatest
// upgrades it to the newest available version. An option misuse is reported
// as a declaration error (resource.Refuse) and nothing is registered.
func Present(name string, opts ...opt.PackageOption) resource.Resource {
	p, err := build(name, opts)
	if err != nil {
		return resource.Refuse("Package", name, err)
	}

	r := resource.Register("Package", p.name, p, p.DependsOn.IDs...)
	resource.RecordPlanDraft(p.planDraft(r.ID()))
	return r
}

// Ensure builds and applies a package resource without registering it or
// recording a plan draft.
func Ensure(name string, opts ...opt.PackageOption) error {
	p, err := build(name, opts)
	if err != nil {
		return err
	}
	return p.apply()
}

// Absent registers a package resource ensuring name is removed.
func Absent(name string, opts ...opt.PackageOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

// build applies opts to a new Package. An option misuse collected while
// applying them (embed.Misuse) is its error.
func build(name string, opts []opt.PackageOption) (*Package, error) {
	p := &Package{name: name}
	for _, o := range opts {
		o.Apply(p)
	}
	if err := p.MisuseErr(); err != nil {
		return nil, err
	}
	return p, nil
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
		Kind:      "package",
		ID:        id,
		Name:      p.name,
		Absent:    p.Absent,
		Latest:    p.latest,
		Deps:      p.DependsOn.SortedIDs(),
		Sensitive: p.Sensitive,
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

// run is p's runner: the plain runner when no WithEnv is set, otherwise the
// environment-aware one with p's variables overlaid on the inherited
// environment (keeping the two separate preserves the legacy unset
// behaviour). It satisfies the runner type the backends are handed.
//
// For a sensitive package (WithSensitive) a failed command's output is
// replaced by a note of its sizes before any backend sees it: every backend
// quotes stdout and stderr in its failure error (runOrErr, dnf's execute),
// and a package manager may echo its environment (a repository URL with
// credentials). Probes only read the exit code, so they are unaffected.
func (p *Package) run(bin string, args ...string) (string, string, int, error) {
	stdout, stderr, code, err := p.runRaw(bin, args...)
	if p.Sensitive && code != 0 {
		stdout, stderr = "", withheldOutput(stdout, stderr)
	}
	return stdout, stderr, code, err
}

// runRaw runs bin through the runner p's environment selects: runCmd, or
// runCmdWithEnv for a package with WithEnv (see run).
func (p *Package) runRaw(bin string, args ...string) (string, string, int, error) {
	if p.env == nil {
		return runCmd(bin, args...)
	}
	return runCmdWithEnv(exec.MergeEnv(p.env), bin, args...)
}

// withheldOutput is the stand-in for a sensitive package command's failure
// output: its sizes only, and why it is withheld.
func withheldOutput(stdout, stderr string) string {
	return fmt.Sprintf("(output withheld: %d bytes stdout, %d bytes stderr; the package operation carries secret material)",
		len(stdout), len(stderr))
}

// runCmd runs a package-manager command for a package without WithEnv: the
// real runner, or the fake a test in this module installed with
// internal/testseam.FakePackageRunner (cross-package apply tests reach the
// backend's dnf/pkg/pkg_add/pkgin invocations that way).
func runCmd(name string, args ...string) (string, string, int, error) {
	if fake := testseam.PackageFakes().Run; fake != nil {
		return fake(name, args...)
	}
	return exec.Run(name, args...)
}

// runCmdWithEnv runs a package-manager command with env, a complete
// environment (the inherited one with the package's WithEnv values
// overlaid): the real runner, or a testseam.FakePackageRunner fake.
func runCmdWithEnv(env []string, name string, args ...string) (string, string, int, error) {
	if fake := testseam.PackageFakes().RunEnv; fake != nil {
		return fake(env, name, args...)
	}
	return exec.RunWith(exec.Opts{Env: env}, name, args...)
}

// detectPkgManager names the host's package manager; selectBackend maps the
// name to a backend. A test in this module can force a name with
// internal/testseam.FakePackageManager (CI runners are often Ubuntu);
// in-package tests may instead hand a backend to applyWith directly.
func detectPkgManager() (string, error) {
	if fake := testseam.PackageManager(); fake != nil {
		return fake()
	}
	return detectPackageManager()
}
