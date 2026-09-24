package pkg

import (
	"errors"
	"fmt"
)

// runner executes one package-manager command and reports its output, exit
// code, and start error. The convergence policy hands every backend call the
// Package's own runner (Package.run), so WithEnv reaches probes and mutations
// alike, and tests can pass a fake runner straight to a backend.
type runner func(bin string, args ...string) (stdout, stderr string, code int, err error)

// command is one package-manager mutation a backend asks the policy to run.
type command struct {
	bin   string   // executable, possibly an absolute path (NetBSD pkgin)
	label string   // tool name used in dry-run and apply log lines
	args  []string // arguments, package name included
	// doneSuffix is appended to the apply log line; dnf sets " completed"
	// to keep its historical "dnf [args] completed" wording.
	doneSuffix string
}

// backend is the OS-specific package-manager mechanism a Package converges
// through. It only knows how to probe and which command performs each
// transition; the policy that decides which transition a desired state needs
// (absent/latest/installed) lives once in Package.applyWith, and dry-run
// handling and result reporting once in the shared runner resource.Converge,
// so the backends no longer each carry a copy of either.
type backend interface {
	// installed reports whether name is currently installed.
	installed(run runner, name string) (bool, error)
	// installCmd installs a package that is not installed.
	installCmd(name string) command
	// upgradeCmd brings name to the newest version (IsLatest). installed is
	// the probe result, for managers whose upgrade verb cannot install.
	upgradeCmd(name string, installed bool) command
	// removeCmd uninstalls an installed package.
	removeCmd(name string) command
	// execute runs c, turning a start failure or non-zero exit into an error.
	execute(run runner, c command) error
}

// backends maps each detector name to its implementation. This table is the
// single place a manager name becomes a backend: supporting another package
// manager means one new backend file plus one entry here, with no change to
// the convergence policy. The name indirection stays because detection, and
// the runners.PackageRunners.Manager override api, plan and resource tests
// inject, speak in names.
var backends = map[string]backend{
	"dnf":     dnfBackend{},
	"freebsd": freebsdBackend{},
	"netbsd":  netbsdBackend{},
	"openbsd": openbsdBackend{},
}

// selectBackend detects the host's package manager (or asks p's injected
// detector) and returns its backend. Detection runs per apply (it is a GOOS
// switch plus a few stat calls), so nothing is cached across applies.
func (p *Package) selectBackend() (backend, error) {
	name, err := p.detectPkgManager()
	if err != nil {
		return nil, err
	}
	b, ok := backends[name]
	if !ok {
		return nil, errors.New("unsupported package manager")
	}
	return b, nil
}

// checkedExec is the execute behaviour shared by the pkg, pkgin and pkg_add
// backends; embed it to get runOrErr's error format. dnf keeps its own
// historical wording and does not embed it.
type checkedExec struct{}

func (checkedExec) execute(run runner, c command) error {
	return runOrErr(run, c.bin, c.args...)
}

// runOrErr runs bin with args, wrapping a start failure and reporting a
// non-zero exit together with the command's stdout and stderr.
func runOrErr(run runner, bin string, args ...string) error {
	stdout, stderr, code, err := run(bin, args...)
	if err != nil {
		return fmt.Errorf("%s %v: %w", bin, args, err)
	}
	if code != 0 {
		return fmt.Errorf("%s %v failed (exit %d): %s%s", bin, args, code, stdout, stderr)
	}
	return nil
}

// probeExitZero runs a probe command whose exit status answers the question:
// 0 means yes, any other exit means no, and a start failure is an error
// prefixed with what (the probe as the operator would type it).
func probeExitZero(run runner, what, bin string, args ...string) (bool, error) {
	_, _, code, err := run(bin, args...)
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	return code == 0, nil
}
