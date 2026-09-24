package service

import (
	"errors"
	"fmt"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/systemd"
)

// The lifecycle verbs the convergence policy can ask for (see verb).
const (
	verbStart   verb = "start"
	verbStop    verb = "stop"
	verbEnable  verb = "enable"
	verbDisable verb = "disable"
	verbRestart verb = "restart"
	verbReload  verb = "reload"
)

var _ resource.Action = backendAction{}

// backends maps each detector name to a constructor for its backend, given
// the run and systemd client a Service resolved (Service.run,
// Service.sysClient): the real ones, or the ones injected through
// newServiceWith for one apply (task 4e2). This table is the single place a
// manager name becomes an implementation: supporting another service
// manager is one new backend file plus one entry here, with no change to
// the policy. In-package tests may build backends directly with their own
// runner instead.
var backends = map[string]func(run runner, sc systemd.Client) backend{
	"systemd": func(_ runner, sc systemd.Client) backend { return systemdBackend{client: sc} },
	"rcctl":   func(run runner, _ systemd.Client) backend { return rcctlBackend{run: run} },
	"freebsd": func(run runner, _ systemd.Client) backend { return freebsdBackend{run: run} },
	"netbsd":  func(run runner, _ systemd.Client) backend { return netbsdBackend{run: run, rcConfD: netbsdRcConfD} },
}

// unit identifies the service a backend acts on: its name and whether it
// lives on the per-user manager (systemd --user; only backends whose
// userSupport returns nil ever see user=true).
type unit struct {
	name string
	user bool
}

// verb is one lifecycle action the convergence policy can ask for.
type verb string

// runner executes one external command and reports its output, exit code
// and start error (the signature of internal/exec.Run).
type runner func(name string, args ...string) (stdout, stderr string, code int, err error)

// backend is the OS-specific service-manager mechanism a Service converges
// through. It only probes state and performs single verbs; the policy that
// turns desired state plus probes into an ordered action list and the change
// gate live once in Service.applyWith, and dry-run handling and result
// reporting once in the shared runner resource.Converge (see runActions), so
// neither is copied into every backend.
type backend interface {
	// userSupport returns nil when WithUser (a per-user manager) works on
	// this backend, otherwise the user-facing reason it does not. The
	// policy only wraps the reason with the service name.
	userSupport() error
	// running reports whether the service is currently running.
	running(u unit) (bool, error)
	// enabled reports whether the service starts at boot.
	enabled(u unit) (bool, error)
	// do performs v on the service.
	do(u unit, v verb) error
	// describe returns the log text for v: would follows "dry-run: would "
	// in a dry run, did is logged once the action succeeded.
	describe(u unit, v verb) (would, did string)
}

// backendAction adapts one verb on a backend to the shared runner's
// resource.Action, so every backend's actions run through resource.Converge.
type backendAction struct {
	b backend
	u unit
	v verb
}

// Do performs the verb through the backend.
func (a backendAction) Do() error { return a.b.do(a.u, a.v) }

// Describe returns the backend's log text for the verb.
func (a backendAction) Describe() (would, did string) { return a.b.describe(a.u, a.v) }

// selectBackend detects the host's service manager and returns its backend,
// wired with s's injected runner and systemd client (nil: the real ones).
// Detection runs per apply (a GOOS switch plus, on Linux, one stat), which
// keeps s's injected detector and runner effective for every apply.
func (s *Service) selectBackend() (backend, error) {
	name, err := s.detectManager()
	if err != nil {
		return nil, err
	}
	newBackend, ok := backends[name]
	if !ok {
		return nil, errors.New("unsupported service manager")
	}
	return newBackend(s.run(), s.sysClient), nil
}

// probeExitZero runs a probe whose exit status answers the question: 0 means
// yes, any other exit means no, and a start failure is an error prefixed
// with what (the probe as an operator would type it).
func probeExitZero(run runner, what, bin string, args ...string) (bool, error) {
	_, _, code, err := run(bin, args...)
	if err != nil {
		return false, fmt.Errorf("%s: %w", what, err)
	}
	return code == 0, nil
}

// runChecked runs bin with args, wrapping a start failure and reporting a
// non-zero exit together with the command's stdout and stderr. Shared by the
// rcctl and FreeBSD service(8) backends.
func runChecked(run runner, bin string, args ...string) error {
	stdout, stderr, code, err := run(bin, args...)
	if err != nil {
		return fmt.Errorf("%s %v: %w", bin, args, err)
	}
	if code != 0 {
		return fmt.Errorf("%s %v failed (exit %d): %s%s", bin, args, code, stdout, stderr)
	}
	return nil
}
