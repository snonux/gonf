// Package pkg implements the package resource with per-OS backends.
package pkg

import (
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"

	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
)

var (
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
	// runFn, runEnvFn and managerFn are the injected overrides of the
	// package-manager runners and the host package-manager detector (see
	// runCmd, runCmdWithEnv and detectPkgManager; nil: the real ones), set
	// by buildWith from a *runners.PackageRunners (task fg2, replacing
	// internal/testseam's process-global FakePackageRunner and
	// FakePackageManager).
	runFn     func(string, ...string) (string, string, int, error)
	runEnvFn  func([]string, string, ...string) (string, string, int, error)
	managerFn func() (string, error)
}

// build applies opts to a new Package using the real runners and detector.
// An option misuse collected while applying them (embed.Misuse) is its
// error.
func build(name string, opts []opt.PackageOption) (*Package, error) {
	return buildWith(nil, name, opts)
}

// buildWith is build with pr's runners and detector injected (nil: the real
// ones): the constructor EnsureWith, and so the package plan.Handler, builds
// with (task fg2, mirroring resource/cmd's newCmdWith).
func buildWith(pr *runners.PackageRunners, name string, opts []opt.PackageOption) (*Package, error) {
	p := &Package{name: name}
	if pr != nil {
		p.runFn = pr.Run
		p.runEnvFn = pr.RunEnv
		p.managerFn = pr.Manager
	}
	for _, o := range opts {
		o.Apply(p)
	}
	if err := p.MisuseErr(); err != nil {
		return nil, err
	}
	return p, nil
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

// Present registers a package resource ensuring name is installed; IsLatest
// upgrades it to the newest available version. An option misuse is reported
// as a declaration error (resource.Refuse) and nothing is registered.
func Present(name string, opts ...opt.PackageOption) resource.Resource {
	p, err := build(name, opts)
	if err != nil {
		return resource.Refuse("Package", name, err)
	}

	r, ok := resource.Register("Package", p.name, p, p.DependsOn.IDs...)
	if ok {
		resource.RecordPlanDraft(p.planDraft(r.ID()))
	}
	return r
}

// Ensure builds and applies a package resource without registering it or
// recording a plan draft.
func Ensure(name string, opts ...opt.PackageOption) error {
	return EnsureWith(nil, name, opts...)
}

// EnsureWith is Ensure with pr's runners and detector (nil: the real ones).
// The plan handler applies through it with this apply's
// plan.ApplyContext.Runners.Package (task fg2). It is exported, unlike
// resource/cmd's ensureWith, for the same reason as cron.EnsureWith: api's
// option-fitness test compares a direct apply against a plan round trip
// with a faked package manager. Only this module can build a
// *runners.PackageRunners, so an external caller can pass nil only.
func EnsureWith(pr *runners.PackageRunners, name string, opts ...opt.PackageOption) error {
	p, err := buildWith(pr, name, opts)
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

// apply selects the host's backend and converges p through it with p's own
// runner (which carries WithEnv). The shared policy lives in applyWith.
func (p *Package) apply() error {
	b, err := p.selectBackend()
	if err != nil {
		return err
	}
	return p.applyWith(b, p.run)
}

// planDraft records p as a "package" plan draft under id. Latest, package's
// one exclusive field, travels in Payload (see Payload, task w62 Layer 1).
func (p *Package) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:      "package",
		ID:        id,
		Name:      p.name,
		Absent:    p.Absent,
		Payload:   Payload{Latest: p.latest},
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
		return p.runCmd(bin, args...)
	}
	return p.runCmdWithEnv(exec.MergeEnv(p.env), bin, args...)
}

// withheldOutput is the stand-in for a sensitive package command's failure
// output: its sizes only, and why it is withheld.
func withheldOutput(stdout, stderr string) string {
	return fmt.Sprintf("(output withheld: %d bytes stdout, %d bytes stderr; the package operation carries secret material)",
		len(stdout), len(stderr))
}

// runCmd runs a package-manager command for a package without WithEnv:
// p.runFn when injected, else the real internal/exec runner.
func (p *Package) runCmd(name string, args ...string) (string, string, int, error) {
	if p.runFn != nil {
		return p.runFn(name, args...)
	}
	return exec.Run(name, args...)
}

// runCmdWithEnv runs a package-manager command with env, a complete
// environment (the inherited one with the package's WithEnv values
// overlaid): p.runEnvFn when injected, else the real internal/exec runner.
func (p *Package) runCmdWithEnv(env []string, name string, args ...string) (string, string, int, error) {
	if p.runEnvFn != nil {
		return p.runEnvFn(env, name, args...)
	}
	return exec.RunWith(exec.Opts{Env: env}, name, args...)
}

// detectPkgManager names the host's package manager; selectBackend maps the
// name to a backend. An injected p.managerFn forces a name (CI runners are
// often Ubuntu); in-package tests may instead hand a backend to applyWith
// directly.
func (p *Package) detectPkgManager() (string, error) {
	if p.managerFn != nil {
		return p.managerFn()
	}
	return detectPackageManager()
}
