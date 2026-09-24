// Package testseam holds the module-internal overrides that let this
// module's tests fake the host commands resource backends run
// (package-manager invocations) and the host's package-manager detection,
// for the resource kinds that have not yet migrated to internal/runners'
// per-apply injection (see that package's doc comment, and AGENTS.md's
// "Test seams" section) — task qb2 shrinks this package one migrated kind
// at a time instead of deleting it in one step, since a kind is only
// removed here once its own production path and every test that faked it
// have moved to a *runners.Set built and passed per apply. resource/cmd
// (the "command" plan kind) migrated first, task qb2's first slice;
// Command/RunOpts/FakeCommand/CommandFakes lived here until then. Task 4e2
// migrated service, timer, daemon_reload and systemd_timer next, all
// through resource/systemd's shared systemctl Client. Task fg2 migrated
// cron (its Crontab/FakeCrontab/FakeCrontabLock/CrontabInProcessLock slots
// became runners.CronRunners, lock choice included): the Package/
// FakePackageRunner/FakePackageManager slots (resource/pkg) remain here
// until package migrates too.
//
// It replaces the exported *ForTest setters the resource packages used to
// carry: being internal, it is importable only from inside this module, so
// the fakes are no longer part of the public API a recipe module sees. Each
// backend resolves its runner here at call time (see e.g. resource/systemd's
// runCmd), which is what lets a cross-package test (api, plan, internal/cli,
// resource) reach a backend through a registered plan handler (api.Apply,
// internal/testapply.Apply) or a direct Ensure without the package handing
// out a setter.
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

// RunEnv runs a command with a complete environment (the inherited process
// environment with a resource's values overlaid).
type RunEnv func(env []string, name string, args ...string) (stdout, stderr string, exitCode int, err error)

// Detect names a host manager ("dnf", "rcctl", "systemd", ...) or fails.
type Detect func() (string, error)

// Package fakes resource/pkg's package-manager runners: Run serves packages
// without WithEnv, RunEnv packages with WithEnv.
type Package struct {
	Run    Run
	RunEnv RunEnv
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
	pkgRunners     slot[Package]
	packageManager slot[Detect]
)

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
