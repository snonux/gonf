// Package service implements the service resource with per-OS backends
// (systemd, OpenBSD rcctl, FreeBSD/NetBSD service(8)).
package service

import (
	"errors"
	"fmt"
	"runtime"
	"slices"

	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/systemd"
)

var (
	_ opt.Absentable      = (*Service)(nil)
	_ opt.Restartable     = (*Service)(nil)
	_ opt.Reloadable      = (*Service)(nil)
	_ opt.UserService     = (*Service)(nil)
	_ opt.Dependable      = (*Service)(nil)
	_ opt.ChangeWatchable = (*Service)(nil)
	_ opt.Flaggable       = (*Service)(nil)
	_ opt.MisuseReporter  = (*Service)(nil)
)

// Service manages a named OS service/daemon.
type Service struct {
	embed.DependsOn
	embed.Absence
	embed.ChangeGate
	embed.Misuse
	name    string
	restart bool
	reload  bool
	user    bool // systemd --user only
	// flags and hasFlags back WithFlags: hasFlags says the flags are managed
	// at all, so WithFlags("") (manage them to empty) differs from no
	// WithFlags (leave them alone).
	flags    string
	hasFlags bool
	// svcRun and svcManager are the injected overrides (nil: the real ones)
	// of runCmd and detectServiceManager, used by the BSD backends and
	// service-manager detection (see run(), detectManager()). sysClient is
	// the systemd runner override the systemd backend uses. Set by
	// newServiceWith from an injected *runners.ServiceRunners/
	// *runners.SystemdRunners (task 4e2, mirroring resource/cmd.Cmd.runFn
	// from qb2) instead of a process-global internal/testseam fake.
	svcRun     runner
	svcManager func() (string, error)
	sysClient  systemd.Client
}

// newService builds a Service with opts applied, using the real runners. An
// option misuse is left in its embed.Misuse for the caller to check.
func newService(name string, opts []opt.ServiceOption) *Service {
	return newServiceWith(nil, nil, name, opts)
}

// newServiceWith is newService with sr/sysR's runners injected (nil: the
// real ones): the service plan.Handler's apply-time constructor (task 4e2,
// mirroring resource/cmd's newCmdWith from qb2) and this package's own
// tests use it directly instead of a package-global fake.
func newServiceWith(sr *runners.ServiceRunners, sysR *runners.SystemdRunners, name string, opts []opt.ServiceOption) *Service {
	s := &Service{name: name, sysClient: systemd.NewClient(sysR)}
	if sr != nil {
		s.svcRun = sr.Run
		s.svcManager = sr.Manager
	}
	for _, o := range opts {
		o.Apply(s)
	}
	return s
}

// SetRestart requests a restart of an already-running present service on
// each apply (subject to the OnChange gate). SetReload wins when both are set.
func (s *Service) SetRestart() { s.restart = true }

// SetReload requests a reload of an already-running present service on each
// apply (subject to the OnChange gate). It takes precedence over SetRestart.
func (s *Service) SetReload() { s.reload = true }

// SetUser targets the systemd --user manager instead of the system one.
// Backends without a user bus reject it at apply time.
func (s *Service) SetUser() { s.user = true }

// SetFlags manages the service's startup flags (see opt.WithFlags).
func (s *Service) SetFlags(flags string) {
	s.flags = flags
	s.hasFlags = true
}

// declarationErr returns the first option misuse, or the WithFlags
// declaration check (checkFlagsDeclaration), for Present and EnsureWith.
func (s *Service) declarationErr() error {
	if err := s.MisuseErr(); err != nil {
		return err
	}
	return s.checkFlagsDeclaration()
}

// Present registers a service that should be running and enabled at boot. An
// option misuse is reported as a declaration error (resource.Refuse) and
// nothing is registered.
func Present(name string, opts ...opt.ServiceOption) resource.Resource {
	s := newService(name, opts)
	if err := s.declarationErr(); err != nil {
		return resource.Refuse("Service", name, err)
	}
	r, ok := resource.Register("Service", s.name, s, s.DependsOn.IDs...)
	if ok {
		resource.RecordPlanDraft(s.planDraft(r.ID()))
	}
	return r
}

// Ensure applies a service without registering it or recording a plan draft.
// An option misuse is returned instead of applied around.
func Ensure(name string, opts ...opt.ServiceOption) error {
	return EnsureWith(nil, nil, name, opts...)
}

// EnsureWith is Ensure with sr/sysR's runners (nil: the real ones) injected
// — the plan handler's apply-time entry (task 4e2, mirroring resource/cmd's
// ensureWith from qb2), instead of a process-global internal/testseam fake.
// Exported (unlike resource/cmd's unexported ensureWith) so a cross-package
// caller that must fake resource/service's backends without a process
// global — such as api's own option-fitness tests — can inject them the
// same way a plan apply does.
func EnsureWith(sr *runners.ServiceRunners, sysR *runners.SystemdRunners, name string, opts ...opt.ServiceOption) error {
	s := newServiceWith(sr, sysR, name, opts)
	if err := s.declarationErr(); err != nil {
		return err
	}
	return s.apply()
}

// Absent registers a service that should be stopped and disabled.
func Absent(name string, opts ...opt.ServiceOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

// apply selects the host's backend and converges s through it. The shared
// policy lives in applyWith (converge.go); backends are in backend.go.
func (s *Service) apply() error {
	b, err := s.selectBackend()
	if err != nil {
		return err
	}
	return s.applyWith(b)
}

// run returns s's injected BSD-backend runner, or the real one.
func (s *Service) run() runner {
	if s.svcRun != nil {
		return s.svcRun
	}
	return runCmd
}

// detectManager returns s's injected service-manager detector, or the real
// one (detectServiceManager).
func (s *Service) detectManager() (string, error) {
	if s.svcManager != nil {
		return s.svcManager()
	}
	return detectServiceManager()
}

// planDraft records s as a "service" plan draft under id, including its
// dependencies and OnChange gate. Reload and the WithFlags flags, service's
// exclusive fields, travel in Payload (see Payload, task w62 Layer 1).
func (s *Service) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:    "service",
		ID:      id,
		Name:    s.name,
		Absent:  s.Absent,
		Restart: s.restart,
		Payload: Payload{Reload: s.reload, Flags: s.flags, HasFlags: s.hasFlags},
		User:    s.user,
		Deps:    s.DependsOn.SortedIDs(),
	}
	d.IfChanged, d.Watch = s.DraftGate()
	return d
}

// detectServiceManager names the service manager for runtime.GOOS: rcctl,
// freebsd or netbsd on the BSDs, and systemd on Linux only when systemd is
// detected. Any other host is an error.
func detectServiceManager() (string, error) {
	switch runtime.GOOS {
	case "openbsd":
		return "rcctl", nil
	case "freebsd":
		return "freebsd", nil
	case "netbsd":
		return "netbsd", nil
	case "linux":
		if systemd.Detected() {
			return "systemd", nil
		}
		return "", errors.New("unable to detect service manager on linux")
	default:
		return "", fmt.Errorf("unable to detect service manager on %s", runtime.GOOS)
	}
}
