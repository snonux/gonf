// Package service implements the service resource with per-OS backends
// (systemd, OpenBSD rcctl, FreeBSD/NetBSD service(8)).
package service

import (
	"errors"
	"fmt"
	"runtime"
	"slices"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	opt "github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/systemd"
)

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

func (s *Service) SetRestart() { s.restart = true }
func (s *Service) SetReload()  { s.reload = true }
func (s *Service) SetUser()    { s.user = true }

var (
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

// gateHolds reports whether the change gate suppresses the restart/reload
// action this apply: the gate is armed (OnChange) and none of the watched
// resources reported a change. State convergence (enable/start/stop) is
// never gated — only the once-per-change action is.
func (s *Service) gateHolds() bool {
	return s.Gated && !resource.AnyChanged(s.Watch...)
}

// Apply runs the service reconciliation directly for the legacy resource path.
func (s *Service) Apply() error { return s.apply() }

// apply selects the host's backend and converges s through it. The shared
// policy lives in applyWith (converge.go); backends are in backend.go.
func (s *Service) apply() error {
	b, err := selectBackend()
	if err != nil {
		return err
	}
	return s.applyWith(b)
}

// newService builds a Service with opts applied.
func newService(name string, opts []opt.ServiceOption) *Service {
	s := &Service{name: name}
	for _, o := range opts {
		o.Apply(s)
	}
	return s
}

// Present registers a service that should be running and enabled at boot.
func Present(name string, opts ...opt.ServiceOption) resource.Resource {
	s := newService(name, opts)
	r := resource.Register("Service", s.name, s, s.DependsOn.IDs...)
	resource.RecordPlanDraft(s.planDraft(r.ID()))
	return r
}

// Ensure applies a service without registering it or recording a plan draft.
func Ensure(name string, opts ...opt.ServiceOption) error {
	return newService(name, opts).apply()
}

// Absent registers a service that should be stopped and disabled.
func Absent(name string, opts ...opt.ServiceOption) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

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
	d.IfChanged = s.Gated
	if d.IfChanged {
		d.Watch = append([]string(nil), s.Watch...)
	}
	return d
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
