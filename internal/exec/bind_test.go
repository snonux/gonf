package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// bindForTest binds ctx for the rest of the test and restores the previous
// binding on cleanup, so a failing test never leaks a canceled context into
// the next one.
func bindForTest(t *testing.T, ctx context.Context) {
	t.Helper()
	t.Cleanup(BindContext(ctx))
}

// Canceling the bound context must kill a command in flight promptly and
// report it as a cancellation (exit -1), not as a timeout or a non-zero exit,
// for every entry point, including the per-call no-timeout opt-out.
func TestBindContextCancelKillsRunningCommand(t *testing.T) {
	cases := []struct {
		name string
		run  func() (string, string, int, error)
	}{
		{"Run", func() (string, string, int, error) { return Run("sleep", "30") }},
		{"RunWithNoTimeout", func() (string, string, int, error) {
			return RunWith(Opts{Timeout: -1}, "sleep", "30")
		}},
		{"RunWithStdin", func() (string, string, int, error) { return RunWithStdin("", "sleep", "30") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			bindForTest(t, ctx)
			time.AfterFunc(100*time.Millisecond, cancel)

			start := time.Now()
			_, _, exitCode, err := tc.run()
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Fatalf("canceled command returned after %v, want prompt return", elapsed)
			}
			if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "canceled") {
				t.Fatalf("err = %v, want a canceled error wrapping context.Canceled", err)
			}
			if exitCode != -1 {
				t.Fatalf("exitCode = %d, want -1", exitCode)
			}
		})
	}
}

// An already canceled bound context refuses to start the command at all.
func TestBindContextCanceledRefusesStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bindForTest(t, ctx)
	stdout, _, exitCode, err := Run("echo", "must-not-run")
	if !errors.Is(err, context.Canceled) || exitCode != -1 || stdout != "" {
		t.Fatalf("Run = (%q, %d, %v), want no output, exit -1, context.Canceled", stdout, exitCode, err)
	}
}

// Negative cases: a live bound context, a nil one and the restored default
// leave commands untouched, and the per-call timeout keeps its "timed out"
// wording under a bound context.
func TestBindContextLeavesNormalRunsAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restore := BindContext(ctx)
	if out, _, code, err := Run("echo", "bound"); err != nil || code != 0 || strings.TrimSpace(out) != "bound" {
		t.Fatalf("bound live ctx: (%q, %d, %v)", out, code, err)
	}
	_, _, _, err := RunWith(Opts{Timeout: 50 * time.Millisecond}, "sleep", "5")
	if err == nil || !strings.Contains(err.Error(), "timed out") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("per-call timeout under bound ctx: err = %v, want timed out", err)
	}
	restore()

	cancel()
	if out, _, code, err := Run("echo", "restored"); err != nil || code != 0 || strings.TrimSpace(out) != "restored" {
		t.Fatalf("after restore the canceled ctx must be unbound: (%q, %d, %v)", out, code, err)
	}

	// A nil ctx is the documented context.Background() fallback.
	bindForTest(t, nil)
	if out, _, code, err := Run("echo", "nil"); err != nil || code != 0 || strings.TrimSpace(out) != "nil" {
		t.Fatalf("nil ctx: (%q, %d, %v)", out, code, err)
	}
}

// withCancelGrace shortens the SIGTERM-to-SIGKILL / pipe-drain grace for
// the rest of the test.
func withCancelGrace(t *testing.T, d time.Duration) {
	t.Helper()
	orig := cancelGrace
	cancelGrace = d
	t.Cleanup(func() { cancelGrace = orig })
}

// runCanceledAfter binds a ctx canceled after delay and runs sh -c script,
// returning its stdout, exit code, error and how long the call took.
func runCanceledAfter(t *testing.T, delay time.Duration, script string) (string, int, time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bindForTest(t, ctx)
	time.AfterFunc(delay, cancel)
	start := time.Now()
	stdout, _, exitCode, err := Run("sh", "-c", script)
	return stdout, exitCode, time.Since(start), err
}

// A canceled command gets SIGTERM first, so a trap (a package manager's
// clean-shutdown path) runs before any SIGKILL.
func TestCancelSendsSIGTERMFirst(t *testing.T) {
	withCancelGrace(t, 500*time.Millisecond)
	stdout, exitCode, _, err := runCanceledAfter(t, 200*time.Millisecond,
		`trap 'echo got-term; exit 3' TERM; sleep 30 & wait`)
	if !strings.Contains(stdout, "got-term") {
		t.Fatalf("stdout %q: the TERM trap did not run", stdout)
	}
	if !errors.Is(err, context.Canceled) || exitCode != -1 {
		t.Fatalf("(%d, %v), want exit -1 and context.Canceled", exitCode, err)
	}
}

// Grandchildren holding the output pipes, and a command ignoring SIGTERM,
// delay a canceled call by about the grace at most, never until they exit.
func TestCancelBoundedByGrace(t *testing.T) {
	cases := map[string]string{
		"grandchild holds pipes": `sleep 5; true`,
		"ignores SIGTERM":        `trap '' TERM; sleep 5; true`,
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			withCancelGrace(t, 300*time.Millisecond)
			_, _, elapsed, err := runCanceledAfter(t, 100*time.Millisecond, script)
			if elapsed > 3*time.Second {
				t.Fatalf("returned after %v, want about delay+grace", elapsed)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		})
	}
}

// Without any cancel, a command that exits 0 while a background grandchild
// keeps its pipes open succeeds after the grace instead of blocking until
// the grandchild exits (exec.ErrWaitDelay is treated as success).
func TestPipeHolderAfterCleanExitSucceeds(t *testing.T) {
	withCancelGrace(t, 300*time.Millisecond)
	start := time.Now()
	stdout, _, exitCode, err := Run("sh", "-c", `sleep 5 & echo done`)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("returned after %v, want about the grace", elapsed)
	}
	if err != nil || exitCode != 0 || strings.TrimSpace(stdout) != "done" {
		t.Fatalf("(%q, %d, %v), want done, 0, nil", stdout, exitCode, err)
	}
}

// A deadline the bound (caller) context carries itself is not reported as
// the per-call timeout, whose duration would be wrong.
func TestCallerDeadlineWording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	bindForTest(t, ctx)
	_, _, _, err := Run("sleep", "5")
	if err == nil || !strings.Contains(err.Error(), "caller deadline exceeded") ||
		strings.Contains(err.Error(), "timed out after") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want caller deadline exceeded", err)
	}
}
