// Package testseam holds the one piece of test-seam state this module still
// shares process-wide: the no-parallel guard for the few test hooks that
// remain intentionally global (internal/testutil.CaptureLog's log capture
// and internal/cli's setTestBaseContext).
//
// It used to hold process-global fakes for the host commands resource
// backends run and the host managers they detect (task 082 moved them here
// from exported *ForTest setters). Every one of those has since moved to
// per-apply injection through internal/runners (see that package's doc
// comment and AGENTS.md's "Test seams" section): command (task qb2), the
// four systemctl kinds (task 4e2), then cron and package (task fg2), which
// removed the last fakes and the layered slot machinery behind them.
//
// What stays global has a reason to: the logger is one process-wide
// destination, and internal/cli's base context is read by the CLI entry
// point no test can hand a context to. A hook like that sets
// ParallelGuardEnv through its Cleaner's Setenv, so testing panics when the
// test, or one of its ancestors, is parallel or later calls t.Parallel.
package testseam

// ParallelGuardEnv is the environment variable every remaining global test
// hook sets through Cleaner.Setenv. Its value is irrelevant: the call exists
// so testing refuses a parallel test that installs process-global state.
const ParallelGuardEnv = "GONF_TESTSEAM"

// Cleaner is the part of testing.TB a global test hook uses: Cleanup to
// restore the state when the test ends and Setenv to make testing refuse the
// hook in a parallel test. *testing.T, *testing.B and testing.TB satisfy it;
// the hooks take this interface instead of testing.TB so production code
// importing this package (internal/cli) does not link the testing package.
type Cleaner interface {
	Cleanup(func())
	Setenv(key, value string)
}
