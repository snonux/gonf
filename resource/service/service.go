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

var (
	_ opt.Absentable      = (*Service)(nil)
	_ opt.Restartable     = (*Service)(nil)
	_ opt.Reloadable      = (*Service)(nil)
	_ opt.UserService     = (*Service)(nil)
	_ opt.Dependable      = (*Service)(nil)
	_ opt.ChangeWatchable = (*Service)(nil)
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
}

// newService builds a Service with opts applied. An option misuse is left in
// its embed.Misuse for the caller to check.
func newService(name string, opts []opt.ServiceOption) *Service {
	s := &Service{name: name}
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

// Present registers a service that should be running and enabled at boot. An
// option misuse is reported as a declaration error (resource.Refuse) and
// nothing is registered.
func Present(name string, opts ...opt.ServiceOption) resource.Resource {
	s := newService(name, opts)
	if err := s.MisuseErr(); err != nil {
		return resource.Refuse("Service", name, err)
	}
	r := resource.Register("Service", s.name, s, s.DependsOn.IDs...)
	resource.RecordPlanDraft(s.planDraft(r.ID()))
	return r
}

// Ensure applies a service without registering it or recording a plan draft.
// An option misuse is returned instead of applied around.
func Ensure(name string, opts ...opt.ServiceOption) error {
	s := newService(name, opts)
	if err := s.MisuseErr(); err != nil {
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
	b, err := selectBackend()
	if err != nil {
		return err
	}
	return s.applyWith(b)
}

// planDraft records s as a "service" plan draft under id, including its
// dependencies and OnChange gate. Reload, service's one exclusive field,
// travels in Payload (see Payload, task w62 Layer 1).
func (s *Service) planDraft(id string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:    "service",
		ID:      id,
		Name:    s.name,
		Absent:  s.Absent,
		Restart: s.restart,
		Payload: Payload{Reload: s.reload},
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
