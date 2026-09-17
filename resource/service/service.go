// Package service implements the service resource with per-OS backends
// (systemd, OpenBSD rcctl, FreeBSD/NetBSD service(8)).
package service

import (
	"errors"
	"fmt"
	"runtime"
	"slices"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	"github.com/snonux/gonf/resource/systemd"
)

// Service manages a named OS service/daemon.
type Service struct {
	embed.DependsOn
	embed.Absence
	name    string
	restart bool
	reload  bool
	user    bool // systemd --user only
}

func (s *Service) SetRestart() { s.restart = true }
func (s *Service) SetReload()  { s.reload = true }
func (s *Service) SetUser()    { s.user = true }

var (
	_ opt.Absentable  = (*Service)(nil)
	_ opt.Restartable = (*Service)(nil)
	_ opt.Reloadable  = (*Service)(nil)
	_ opt.UserService = (*Service)(nil)
	_ opt.Dependable  = (*Service)(nil)
)

// detectSvcManager is swapped in unit tests (mirrors resource/pkg's
// detectPkgManager), so the fitness test can force the BSD/rcctl backends
// (freebsd/netbsd/rcctl) on any single host instead of only ever reaching
// whichever backend runtime.GOOS happens to select.
var detectSvcManager = detectServiceManager

func (s *Service) apply() error {
	mgr, err := detectSvcManager()
	if err != nil {
		return err
	}
	if s.user && mgr != "systemd" {
		return fmt.Errorf("Service[%s]: WithUser is only supported on systemd", s.name)
	}

	switch mgr {
	case "systemd":
		return applySystemd(s)
	case "rcctl":
		return applyRcctl(s)
	case "freebsd":
		return applyFreeBSD(s)
	case "netbsd":
		return applyNetBSD(s)
	default:
		return errors.New("unsupported service manager")
	}
}

// newService builds a Service with opts applied.
func newService(name string, opts []opt.Option) *Service {
	s := &Service{name: name}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Present registers a service that should be running and enabled at boot.
func Present(name string, opts ...opt.Option) resource.Resource {
	s := newService(name, opts)
	r := resource.Register("Service", s.name,
		resource.ApplierFunc(func() error { return s.apply() }), s.DependsOn.IDs...)
	resource.RecordPlanDraft(s.planDraft(r.ID()))
	return r
}

// Ensure applies a service without registering it or recording a plan draft.
func Ensure(name string, opts ...opt.Option) error {
	return newService(name, opts).apply()
}

// Absent registers a service that should be stopped and disabled.
func Absent(name string, opts ...opt.Option) resource.Resource {
	opts = append(slices.Clone(opts), opt.IsAbsent)
	return Present(name, opts...)
}

func (s *Service) planDraft(id string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:    "service",
		ID:      id,
		Name:    s.name,
		Absent:  s.Absent,
		Restart: s.restart,
		Reload:  s.reload,
		User:    s.user,
		Deps:    s.DependsOn.SortedIDs(),
	}
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
