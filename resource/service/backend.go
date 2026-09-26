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
	// verbDisableRuntime removes a runtime-only enablement (systemctl
	// disable --runtime). Only the systemd backend's enablement probe asks
	// for it (see enablementProber).
	verbDisableRuntime verb = "disable --runtime"
)

var _ resource.Action = backendAction{}

// backendDeps is everything a backend constructor may need from the Service
// that selected it (Service.backendDeps): the command runner the BSD
// backends use and the systemd client the systemd backend uses, either the
// real ones or the ones injected through newServiceWith for one apply (task
// 4e2). Each constructor reads only the field it needs. The dependencies
// travel as one struct, not as positional parameters (task rg2): with
// parameters, every entry had to accept (and discard) the dependencies of
// every other backend, a new backend with a third dependency (a launchd
// client, say) widened the signature of all of them again, and the
// function type could not tell two same-shaped parameters apart at a call
// site. A new dependency is one new field here instead.
type backendDeps struct {
	run     runner
	systemd systemd.Client
}

// backends maps each detector name to a constructor for its backend, given
// the dependencies a Service resolved (Service.backendDeps). This table is
// the single place a manager name becomes an implementation: supporting
// another service manager is one new backend file plus one entry here, with
// no change to the policy. In-package tests may build backends directly
// with their own runner instead.
var backends = map[string]func(d backendDeps) backend{
	"systemd": func(d backendDeps) backend { return systemdBackend{client: d.systemd} },
	"rcctl":   func(d backendDeps) backend { return rcctlBackend{run: d.run} },
	"freebsd": func(d backendDeps) backend { return freebsdBackend{run: d.run} },
	"netbsd": func(d backendDeps) backend {
		return netbsdBackend{run: d.run, rcConfD: netbsdRcConfD, rcConf: netbsdRcConf, rcConfDefaults: netbsdRcConfDefaults}
	},
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

// enablementProber is an optional backend capability for a manager whose
// "enabled at boot" probe can be true for a unit that disable cannot change
// (a systemd static unit), or whose enablement needs another verb to remove
// (systemd enabled-runtime). The policy then probes through it instead of
// backend.enabled: enabled says the unit needs no enable, disable is the
// verb that removes its enablement ("" when there is nothing disable can
// change).
type enablementProber interface {
	enablement(u unit) (enabled bool, disable verb, err error)
}

// probeEnablement probes u's boot-time enablement through b: its
// enablementProber when it has one, otherwise backend.enabled, whose true
// is removed by verbDisable.
func probeEnablement(b backend, u unit) (enabled bool, disable verb, err error) {
	if p, ok := b.(enablementProber); ok {
		return p.enablement(u)
	}
	enabled, err = b.enabled(u)
	if enabled {
		disable = verbDisable
	}
	return enabled, disable, err
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
// wired with s's dependencies (s.backendDeps: the injected runner and
// systemd client, or the real ones).
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
	return newBackend(s.backendDeps()), nil
}

// backendDeps resolves s's backend dependencies once per selection: the
// injected BSD runner or the real one (s.run), and s's systemd client
// (whose zero value already is the real one).
func (s *Service) backendDeps() backendDeps {
	return backendDeps{run: s.run(), systemd: s.sysClient}
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
