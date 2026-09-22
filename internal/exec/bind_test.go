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
