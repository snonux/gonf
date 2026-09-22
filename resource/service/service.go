// Package service implements the service resource with per-OS backends
// (systemd, OpenBSD rcctl, FreeBSD/NetBSD service(8)).
package service

import (
	"errors"
	"fmt"
	"runtime"
	"slices"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/systemd"
)

var (
	// Register takes the value as a resource.Applier; asserting it here reports a
	// renamed or re-signed Apply at the declaration, not at the Register call.
	_ resource.Applier    = (*Service)(nil)
	_ opt.Absentable      = (*Service)(nil)
	_ opt.Restartable     = (*Service)(nil)
	_ opt.Reloadable      = (*Service)(nil)
	_ opt.UserService     = (*Service)(nil)
	_ opt.Dependable      = (*Service)(nil)
	_ opt.ChangeWatchable = (*Service)(nil)
)

// detectSvcManager names the host's service manager; selectBackend maps the
// name to a backend. It is swapped by SetDetectServiceManagerForTest
// (mirrors resource/pkg's detectPkgManager), so the fitness test can force
// the BSD/rcctl backends on any single host instead of only ever reaching
// whichever backend runtime.GOOS happens to select. In-package tests instead
// hand a backend to applyWith directly.
var detectSvcManager = detectServiceManager

// Service manages a named OS service/daemon.
type Service struct {
	embed.DependsOn
	embed.Absence
	embed.ChangeGate
	name    string
	restart bool
	reload  bool
	user    bool // systemd --user only
}

// newService builds a Service with opts applied. The error reports a change
// gate armed with nothing to watch (the legacy IfChanged reaching a Service
// through the type-erased Option path): it could never fire, so Present
// aborts and Ensure fails instead of holding restarts forever.
func newService(name string, opts []opt.ServiceOption) (*Service, error) {
	s := &Service{name: name}
	for _, o := range opts {
		o.Apply(s)
	}
	if err := s.CheckWatch(); err != nil {
		return s, fmt.Errorf("%s: %w", resource.FormatID("Service", name), err)
	}
	return s, nil
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

// Apply runs the service reconciliation directly for the legacy resource path.
func (s *Service) Apply() error { return s.apply() }

// Present registers a service that should be running and enabled at boot.
func Present(name string, opts ...opt.ServiceOption) resource.Resource {
	s, err := newService(name, opts)
	if err != nil {
		logger.Fatal("%v", err)
	}
	r := resource.Register("Service", s.name, s, s.DependsOn.IDs...)
	resource.RecordPlanDraft(s.planDraft(r.ID()))
	return r
}

// Ensure applies a service without registering it or recording a plan draft.
func Ensure(name string, opts ...opt.ServiceOption) error {
	s, err := newService(name, opts)
	if err != nil {
		return err
	}
	return s.apply()
}

// Absent registers a service that should be stopped and disabled.
func Absent(name string, opts ...opt.ServiceOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

// SetDetectServiceManagerForTest stubs OS service-manager detection (tests
// only), mirroring resource/pkg's SetDetectPackageManagerForTest.
func SetDetectServiceManagerForTest(fn func() (string, error)) {
	detectSvcManager = fn
}

// ResetDetectServiceManagerForTest restores the real detector after a test
// stub.
func ResetDetectServiceManagerForTest() {
	detectSvcManager = detectServiceManager
}

// apply selects the host's backend and converges s through it. The shared
// policy lives in applyWith (converge.go); backends are in backend.go.
func (s *Service) apply() error {
	b, err := selectBackend()
	if err != nil {
		return err
	}
	return s.applyWith(b)
}

// planDraft records s as a "service" plan draft under id, including its
// dependencies and OnChange gate.
func (s *Service) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:    "service",
		ID:      id,
		Name:    s.name,
		Absent:  s.Absent,
		Restart: s.restart,
		Reload:  s.reload,
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
