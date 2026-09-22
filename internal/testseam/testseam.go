// Package testseam holds the module-internal overrides that let this
// module's tests fake the host commands resource backends run (command,
// crontab, package-manager, service-manager and systemctl invocations) and
// the host's package- and service-manager detection.
//
// It replaces the exported *ForTest setters the resource packages used to
// carry: being internal, it is importable only from inside this module, so
// the fakes are no longer part of the public API a recipe module sees. Each
// backend resolves its runner here at call time (see e.g. resource/systemd's
// runCmd), which is what lets a cross-package test (api, plan, internal/cli,
// resource) reach a backend through a registered plan handler, the legacy
// resource.Apply path or a direct Ensure without the package handing out a
// setter.
//
// In production nothing is ever installed and every accessor returns its
// zero value, so the backends use the real internal/exec runners. A Fake*
// call installs its override until the Cleaner's cleanup runs, restoring
// whatever was installed before, so nested fakes unwind in order. Fakes are
// process-global: a test using one must not run in parallel with another
// test that reaches the same backend.
package testseam

import (
	"sync"

	"github.com/snonux/gonf/internal/exec"
)

// Cleaner registers a function to run when a test ends. *testing.T,
// *testing.B and testing.TB satisfy it; the package takes this one-method
// interface instead of testing.TB so production code importing it does not
// link the testing package.
type Cleaner interface {
	Cleanup(func())
}

// Run is the signature of internal/exec.Run.
type Run func(name string, args ...string) (stdout, stderr string, exitCode int, err error)

// RunOpts is the signature of internal/exec.RunWith.
type RunOpts func(opts exec.Opts, name string, args ...string) (stdout, stderr string, exitCode int, err error)

// RunEnv runs a command with a complete environment (the inherited process
// environment with a resource's values overlaid).
type RunEnv func(env []string, name string, args ...string) (stdout, stderr string, exitCode int, err error)

// RunStdin is the signature of internal/exec.RunWithStdin.
type RunStdin func(stdin, name string, args ...string) (stdout, stderr string, exitCode int, err error)

// Detect names a host manager ("dnf", "rcctl", "systemd", ...) or fails.
type Detect func() (string, error)

// Command fakes resource/cmd's runners: Run runs the main command (it
// carries Dir/Env opts), Probe runs the Unless/OnlyIf guard probes.
type Command struct {
	Run   RunOpts
	Probe Run
}

// Crontab fakes resource/cron's crontab(1) runners: Read runs crontab -l,
// Write runs crontab - with the new table on stdin. RealLock keeps the real
// cross-process crontab lock while the runners are faked (resource/cron's
// own lock tests); otherwise an in-process lock replaces it.
type Crontab struct {
	Read     Run
	Write    RunStdin
	RealLock bool
}

// Package fakes resource/pkg's package-manager runners: Run serves packages
// without WithEnv, RunEnv packages with WithEnv.
type Package struct {
	Run    Run
	RunEnv RunEnv
}

// crontabFake is the crontab slot's value: the fakes plus whether any
// FakeCrontab call is in effect (which also swaps the cross-process lock).
type crontabFake struct {
	Crontab
	on bool
}

// slot is one override, guarded so a fake installed by the test goroutine
// is safely read by apply code on another goroutine.
type slot[T any] struct {
	mu sync.Mutex
	v  T
}

var (
	command        slot[Command]
	crontab        slot[crontabFake]
	pkgRunners     slot[Package]
	packageManager slot[Detect]
	serviceRun     slot[Run]
	serviceManager slot[Detect]
	systemctl      slot[Run]
)

// FakeCommand installs f's non-nil runners for resource/cmd until c's
// cleanup; a nil field keeps the runner currently in effect for that slot.
func FakeCommand(c Cleaner, f Command) {
	command.swap(c, func(cur Command) Command {
		if f.Run != nil {
			cur.Run = f.Run
		}
		if f.Probe != nil {
			cur.Probe = f.Probe
		}
		return cur
	})
}

// CommandFakes returns the resource/cmd fakes in effect (nil fields: real).
func CommandFakes() Command { return command.get() }

// FakeCrontab installs f's non-nil crontab runners for resource/cron until
// c's cleanup (a nil field keeps the runner currently in effect). While any
// crontab fake is installed, resource/cron also swaps its cross-process
// crontab lock for an in-process one unless the latest call set RealLock: a
// faked crontab is not shared with other processes, and the real lock would
// create state in the test user's home directory from every package that
// fakes cron.
func FakeCrontab(c Cleaner, f Crontab) {
	crontab.swap(c, func(cur crontabFake) crontabFake {
		if f.Read != nil {
			cur.Read = f.Read
		}
		if f.Write != nil {
			cur.Write = f.Write
		}
		cur.RealLock = f.RealLock
		cur.on = true
		return cur
	})
}

// CrontabFakes returns the resource/cron fakes in effect (nil fields: real)
// and whether a FakeCrontab call is in effect at all.
func CrontabFakes() (Crontab, bool) {
	f := crontab.get()
	return f.Crontab, f.on
}

// FakePackageRunner installs f's non-nil runners for resource/pkg until c's
// cleanup; a nil field keeps the runner currently in effect for that slot.
func FakePackageRunner(c Cleaner, f Package) {
	pkgRunners.swap(c, func(cur Package) Package {
		if f.Run != nil {
			cur.Run = f.Run
		}
		if f.RunEnv != nil {
			cur.RunEnv = f.RunEnv
		}
		return cur
	})
}

// PackageFakes returns the resource/pkg runner fakes in effect (nil: real).
func PackageFakes() Package { return pkgRunners.get() }

// FakePackageManager makes resource/pkg's backend selection use d instead of
// detecting the host's package manager, until c's cleanup. CI runners are
// often Ubuntu, so tests force a backend by name.
func FakePackageManager(c Cleaner, d Detect) {
	packageManager.swap(c, func(Detect) Detect { return d })
}

// PackageManager returns the resource/pkg detector fake in effect, or nil.
func PackageManager() Detect { return packageManager.get() }

// FakeServiceRunner installs run as resource/service's command runner until
// c's cleanup: for the BSD backends (rcctl, service(8)) and, through
// FakeSystemctl, for the systemctl backend, so a service follows the fake
// whichever backend the host selects.
func FakeServiceRunner(c Cleaner, run Run) {
	serviceRun.swap(c, func(Run) Run { return run })
	FakeSystemctl(c, run)
}

// ServiceRunner returns the BSD service backends' runner fake, or nil.
func ServiceRunner() Run { return serviceRun.get() }

// FakeServiceManager makes resource/service's backend selection use d
// instead of detecting the host's service manager, until c's cleanup.
func FakeServiceManager(c Cleaner, d Detect) {
	serviceManager.swap(c, func(Detect) Detect { return d })
}

// ServiceManager returns the resource/service detector fake in effect, or
// nil.
func ServiceManager() Detect { return serviceManager.get() }

// FakeSystemctl installs run as the shared systemctl runner in
// resource/systemd (used by services on systemd, timers, systemd timers and
// DaemonReload) until c's cleanup.
func FakeSystemctl(c Cleaner, run Run) {
	systemctl.swap(c, func(Run) Run { return run })
}

// Systemctl returns the systemctl runner fake in effect, or nil.
func Systemctl() Run { return systemctl.get() }

// get returns the slot's current value.
func (s *slot[T]) get() T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v
}

// swap replaces the slot's value with update(current) and registers a
// cleanup on c that restores the value it replaced.
func (s *slot[T]) swap(c Cleaner, update func(T) T) {
	s.mu.Lock()
	prev := s.v
	s.v = update(prev)
	s.mu.Unlock()
	c.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.v = prev
	})
}
