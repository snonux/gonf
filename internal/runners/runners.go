// Package runners carries per-apply overrides for the backend runners plan
// handlers use in place of the real ones (internal/exec), for exactly one
// apply instead of a process-global fake (see internal/testseam, which this
// package replaces one resource kind at a time — task qb2, the follow-up to
// 082's own doc comment on the drift it accepted between relocating a global
// and actually injecting).
//
// A *Set travels two ways, both scoped to a single apply:
//
//   - through plan.ApplyContext.Runners, threaded by plan.ApplyWithContext
//     from the context passed to it (see WithSet/FromContext below) down to
//     every plan.Handler.Apply call for that run;
//   - directly, as a constructor argument, on the resource package's own
//     "…With" constructor (e.g. resource/cmd's newCmdWith), the same shape
//     resource/user's newUserWith (task 372) already established.
//
// Both paths end up building the resource with the same *runners.
// CommandRunners (etc.) value, so a plan-path apply and a direct Ensure of
// the same kind converge through identical code.
//
// Only code inside this module can import this package (it lives under
// internal/) and only this package can construct the unexported context key
// WithSet/FromContext use, so an external recipe module can read a
// plan.ApplyContext.Runners field but can never populate one: the zero value
// (nil) is the only value it can ever observe or pass on, which is exactly
// "use the real runner".
package runners

import (
	"context"

	"github.com/snonux/gonf/internal/exec"
)

// Set groups the runner overrides for every resource kind that has migrated
// off internal/testseam. A nil Set, or a nil field within one, means "use
// the real runner" for that kind: handlers check for nil before consulting
// a field, so a Set that only overrides one kind (e.g. Command) leaves every
// other kind's real backend untouched. Extend this struct one field per
// kind as each kind migrates (see docs/plan.md's Test seams note and
// AGENTS.md's Test seams section).
type Set struct {
	// Command overrides resource/cmd's Cmd (the "command" plan kind).
	Command *CommandRunners
	// Systemd overrides resource/systemd's shared systemctl Client (task
	// 4e2): consulted by Service's systemd backend, Timer and DaemonReload
	// directly, and by SystemdTimer through its Timer/DaemonReload
	// composition.
	Systemd *SystemdRunners
	// Service overrides resource/service's own touch points beyond the
	// shared systemd client (task 4e2): the BSD backends' (rcctl,
	// FreeBSD/NetBSD service(8)) command runner and the host
	// service-manager detector.
	Service *ServiceRunners
}

// CommandRunners overrides resource/cmd's two external touch points: Run
// executes the main command (it carries Dir/Env via exec.Opts), Probe runs
// an Unless/OnlyIf guard probe. Both mirror internal/exec's Run/RunWith
// signatures (see resource/cmd's runWith/runProbe, the real default used
// when a field is nil, whether because Set itself is nil or Command is).
type CommandRunners struct {
	Run   func(opts exec.Opts, name string, args ...string) (stdout, stderr string, exitCode int, err error)
	Probe func(name string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// SystemdRunners overrides resource/systemd's shared systemctl invocation
// (the same shape internal/exec.Run has, and resource/systemd's own
// RunFunc): every systemctl helper in that package (the Client methods
// IsActive, IsEnabled, Run and Command; there are no package-level
// shortcuts that could bypass it) funnels through it via a
// resource/systemd.Client built with NewClient(sr).
type SystemdRunners struct {
	Run func(name string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// ServiceRunners overrides resource/service's own touch points that do not
// go through resource/systemd's Client: Run drives the BSD backends (rcctl,
// FreeBSD/NetBSD service(8)) the same way internal/exec.Run would, Manager
// overrides host service-manager detection (detectServiceManager).
type ServiceRunners struct {
	Run     func(name string, args ...string) (stdout, stderr string, exitCode int, err error)
	Manager func() (name string, err error)
}

// ctxKey is the unexported type of the context key WithSet/FromContext
// share; being unexported, no other package (in or out of this module) can
// collide with it or forge a value FromContext would return.
type ctxKey struct{}

// WithSet returns a context carrying s for FromContext to find, so a caller
// several layers up the call stack (api's own unexported applyWithRunners,
// internal/testapply.ApplyWithRunners, a plan-package test calling
// plan.ApplyWithContext directly) can inject runners for one apply without a
// package-global variable. A nil s returns ctx unchanged (no-op), so
// WithSet(ctx, nil) is always safe and never shadows an outer Set with an
// empty one.
func WithSet(ctx context.Context, s *Set) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the Set WithSet attached to ctx, or nil when none was
// (production applies: every handler falls back to its real runner).
func FromContext(ctx context.Context) *Set {
	s, _ := ctx.Value(ctxKey{}).(*Set)
	return s
}

// CommandOf returns s.Command, nil-safe for a nil s: every call site that
// only needs the command override reads it this way (from a *Set that may
// itself be nil, e.g. plan.ApplyContext.Runners in a production apply)
// instead of a bare, potentially nil-pointer-dereferencing field access.
func CommandOf(s *Set) *CommandRunners {
	if s == nil {
		return nil
	}
	return s.Command
}

// SystemdOf returns s.Systemd, nil-safe for a nil s.
func SystemdOf(s *Set) *SystemdRunners {
	if s == nil {
		return nil
	}
	return s.Systemd
}

// ServiceOf returns s.Service, nil-safe for a nil s.
func ServiceOf(s *Set) *ServiceRunners {
	if s == nil {
		return nil
	}
	return s.Service
}
