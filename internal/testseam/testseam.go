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
// call installs its override as one layer until the Cleaner's cleanup runs;
// the value in effect is every installed layer applied in install order, and
// a cleanup removes exactly its own layer, so fakes unwind correctly whatever
// order their cleanups run in.
//
// Fakes are process-global, so a test using one must not run in parallel:
// every Fake* call also calls the Cleaner's Setenv (GONF_TESTSEAM=1), and
// testing's own check then panics when the test, or one of its ancestors, is
// parallel or later calls t.Parallel.
package testseam

import (
	"sync"

	"github.com/snonux/gonf/internal/exec"
)

// ParallelGuardEnv is the environment variable every Fake* call sets through
// Cleaner.Setenv. Its value is irrelevant: the call exists so testing refuses
// a parallel test that installs a process-global fake.
const ParallelGuardEnv = "GONF_TESTSEAM"

// Cleaner is the part of testing.TB the fakes use: Cleanup to remove the
// fake when the test ends and Setenv to make testing refuse the fake in a
// parallel test. *testing.T, *testing.B and testing.TB satisfy it; the
// package takes this interface instead of testing.TB so production code
// importing it does not link the testing package.
type Cleaner interface {
	Cleanup(func())
	Setenv(key, value string)
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
// Write runs crontab - with the new table on stdin.
type Crontab struct {
	Read  Run
	Write RunStdin
}

// Package fakes resource/pkg's package-manager runners: Run serves packages
// without WithEnv, RunEnv packages with WithEnv.
type Package struct {
	Run    Run
	RunEnv RunEnv
}

// crontabFake is the crontab slot's value: the fakes plus whether any
// FakeCrontab layer is installed (which also selects the in-process lock
// unless a FakeCrontabLock layer decides otherwise).
type crontabFake struct {
	Crontab
	on bool
}

// lockChoice is the crontab-lock slot's value: whether a FakeCrontabLock
// layer is installed and, if so, whether it asked for the in-process lock.
type lockChoice struct {
	set       bool
	inProcess bool
}

// layer is one installed fake: update derives the slot's value from the
// value below it.
type layer[T any] struct {
	update func(T) T
}

// slot is one override: a stack of layers, guarded so a fake installed by
// the test goroutine is safely read by apply code on another goroutine.
type slot[T any] struct {
	mu     sync.Mutex
	layers []*layer[T]
}

var (
	command        slot[Command]
	crontab        slot[crontabFake]
	crontabLock    slot[lockChoice]
	pkgRunners     slot[Package]
	packageManager slot[Detect]
	serviceRun     slot[Run]
	serviceManager slot[Detect]
	systemctl      slot[Run]
)

// FakeCommand installs f's non-nil runners for resource/cmd until c's
// cleanup; a nil field keeps the runner currently in effect for that slot.
func FakeCommand(c Cleaner, f Command) {
	command.push(c, func(cur Command) Command {
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
// crontab fake is installed, resource/cron takes an in-process crontab lock
// instead of the cross-process one, unless FakeCrontabLock chose otherwise:
// a faked crontab is not shared with other processes, and the real lock
// would create state in the test user's home directory from every package
// that fakes cron.
func FakeCrontab(c Cleaner, f Crontab) {
	crontab.push(c, func(cur crontabFake) crontabFake {
		if f.Read != nil {
			cur.Read = f.Read
		}
		if f.Write != nil {
			cur.Write = f.Write
		}
		cur.on = true
		return cur
	})
}

// CrontabFakes returns the resource/cron fakes in effect (nil fields: real).
func CrontabFakes() Crontab { return crontab.get().Crontab }

// FakeCrontabLock decides, until c's cleanup, which lock resource/cron takes
// for a crontab transaction: the in-process one (inProcess) or the real
// cross-process one. resource/cron's own lock tests use it to keep the real
// lock while the crontab runners are faked. Being its own slot, a later
// FakeCrontab cannot silently change the choice.
func FakeCrontabLock(c Cleaner, inProcess bool) {
	crontabLock.push(c, func(lockChoice) lockChoice {
		return lockChoice{set: true, inProcess: inProcess}
	})
}

// CrontabInProcessLock reports whether resource/cron should take its
// in-process lock: the FakeCrontabLock choice when one is installed,
// otherwise whether any FakeCrontab is installed.
func CrontabInProcessLock() bool {
	if l := crontabLock.get(); l.set {
		return l.inProcess
	}
	return crontab.get().on
}

// FakePackageRunner installs f's non-nil runners for resource/pkg until c's
// cleanup; a nil field keeps the runner currently in effect for that slot.
func FakePackageRunner(c Cleaner, f Package) {
	pkgRunners.push(c, func(cur Package) Package {
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
	packageManager.push(c, func(Detect) Detect { return d })
}

// PackageManager returns the resource/pkg detector fake in effect, or nil.
func PackageManager() Detect { return packageManager.get() }

// FakeServiceRunner installs run as resource/service's command runner until
// c's cleanup: for the BSD backends (rcctl, service(8)) and, through
// FakeSystemctl, for the systemctl backend, so a service follows the fake
// whichever backend the host selects.
func FakeServiceRunner(c Cleaner, run Run) {
	serviceRun.push(c, func(Run) Run { return run })
	FakeSystemctl(c, run)
}

// ServiceRunner returns the BSD service backends' runner fake, or nil.
func ServiceRunner() Run { return serviceRun.get() }

// FakeServiceManager makes resource/service's backend selection use d
// instead of detecting the host's service manager, until c's cleanup.
func FakeServiceManager(c Cleaner, d Detect) {
	serviceManager.push(c, func(Detect) Detect { return d })
}

// ServiceManager returns the resource/service detector fake in effect, or
// nil.
func ServiceManager() Detect { return serviceManager.get() }

// FakeSystemctl installs run as the shared systemctl runner in
// resource/systemd (used by services on systemd, timers, systemd timers and
// DaemonReload) until c's cleanup.
func FakeSystemctl(c Cleaner, run Run) {
	systemctl.push(c, func(Run) Run { return run })
}

// Systemctl returns the systemctl runner fake in effect, or nil.
func Systemctl() Run { return systemctl.get() }

// get returns the slot's value: every installed layer applied, oldest first,
// to the zero value.
func (s *slot[T]) get() T {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v T
	for _, l := range s.layers {
		v = l.update(v)
	}
	return v
}

// push installs update as a new top layer, after refusing a parallel test
// through c.Setenv, and registers a cleanup on c that removes exactly this
// layer (by identity, so an out-of-order cleanup cannot resurrect or drop
// another test's fake).
func (s *slot[T]) push(c Cleaner, update func(T) T) {
	c.Setenv(ParallelGuardEnv, "1")
	l := &layer[T]{update: update}
	s.mu.Lock()
	s.layers = append(s.layers, l)
	s.mu.Unlock()
	c.Cleanup(func() { s.remove(l) })
}

// remove drops layer l from the stack, wherever it is.
func (s *slot[T]) remove(l *layer[T]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, cur := range s.layers {
		if cur == l {
			s.layers = append(s.layers[:i:i], s.layers[i+1:]...)
			return
		}
	}
}
