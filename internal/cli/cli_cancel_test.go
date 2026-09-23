package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// promptReturn bounds how long a canceled apply may take to return. The
// long-running command sleeps 30s, so anything near that means the cancel
// did not reach it.
const promptReturn = 10 * time.Second

// startedScript touches started, then becomes a 30s sleep: a test can wait
// for started to know the long-running command is in flight.
func startedScript(started string) []string {
	return []string{"-c", "touch '" + started + "'; exec sleep 30"}
}

// sleepThenTouchOps is a plan whose first op blocks for 30s (after touching
// started) and whose second op touches marker, so a test can tell whether
// the apply stopped after the canceled command.
func sleepThenTouchOps(started, marker string) []plan.Op {
	return []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "cancel"},
		{Op: plan.KindCommand, Bin: "sh", Args: startedScript(started), ID: "Command[sleep]"},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[touch]"},
	}
}

// writePlanFile encodes ops into dir/plan.jsonl (mode 0600) and returns it.
func writePlanFile(t *testing.T, dir string, ops []plan.Op) string {
	t.Helper()
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plan.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// feedStdin replaces os.Stdin with a pipe carrying data for the test.
func feedStdin(t *testing.T, data []byte) {
	t.Helper()
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	go func() {
		_, _ = w.Write(data)
		_ = w.Close()
	}()
}

// requireInterrupted checks the shape of a canceled apply: exit 1, prompt
// return, the interrupted wording on stderr and no op after the killed one.
func requireInterrupted(t *testing.T, code int, elapsed time.Duration, stderr, wantPrefix, marker string) {
	t.Helper()
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr:\n%s", code, stderr)
	}
	if elapsed > promptReturn {
		t.Fatalf("canceled apply returned after %v, want prompt return", elapsed)
	}
	if !strings.Contains(stderr, wantPrefix) || !strings.Contains(stderr, "context canceled") {
		t.Fatalf("stderr %q lacks %q and the cancellation", stderr, wantPrefix)
	}
	requireNotTouched(t, marker)
}

// whenStarted runs fn once the file started exists (the long-running
// command is in flight), polling for up to promptReturn; t.Cleanup stops
// the polling.
func whenStarted(t *testing.T, started string, fn func()) {
	t.Helper()
	quit := make(chan struct{})
	t.Cleanup(func() { close(quit) })
	go func() {
		deadline := time.Now().Add(promptReturn)
		for time.Now().Before(deadline) {
			select {
			case <-quit:
				return
			case <-time.After(10 * time.Millisecond):
			}
			if _, err := os.Stat(started); err == nil {
				fn()
				return
			}
		}
	}()
}

// cancelWhenStarted returns a ctx canceled once started exists.
func cancelWhenStarted(t *testing.T, started string) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	whenStarted(t, started, cancel)
	return ctx
}

// signalWhenStarted sends sig to this process once started exists, i.e.
// while CLI() runs the long command (so its NotifyContext is installed). The
// test also subscribes to sig for its duration, so the signal can never fall
// back to its default action (killing the test binary).
func signalWhenStarted(t *testing.T, sig syscall.Signal, started string) {
	t.Helper()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sig)
	t.Cleanup(func() { signal.Stop(ch) })
	whenStarted(t, started, func() { _ = syscall.Kill(os.Getpid(), sig) })
}

// TestCLIApplyFileCanceledByContext: canceling the CLI context while
// `gonf apply <plan.jsonl>` runs a long command stops it and fails the apply.
func TestCLIApplyFileCanceledByContext(t *testing.T) {
	root := t.TempDir()
	started, marker := filepath.Join(root, "started"), filepath.Join(root, "after")
	path := writePlanFile(t, root, sleepThenTouchOps(started, marker))
	ctx := cancelWhenStarted(t, started)

	var code int
	start := time.Now()
	stderr := testutil.CaptureStderr(t, func() { code = cliApply(ctx, []string{path}) })
	requireInterrupted(t, code, time.Since(start), stderr, "apply: interrupted: ", marker)
}

// TestCLIApplyStdinCanceledByContext: the receiving end of a push (`gonf
// apply -`) is canceled the same way.
func TestCLIApplyStdinCanceledByContext(t *testing.T) {
	root := t.TempDir()
	started, marker := filepath.Join(root, "started"), filepath.Join(root, "after")
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, sleepThenTouchOps(started, marker), nil); err != nil {
		t.Fatal(err)
	}
	feedStdin(t, buf.Bytes())
	ctx := cancelWhenStarted(t, started)

	var code int
	start := time.Now()
	stderr := testutil.CaptureStderr(t, func() { code = cliApply(ctx, []string{"-"}) })
	requireInterrupted(t, code, time.Since(start), stderr, "apply: interrupted: ", marker)
}

// TestCLIApplyInterruptedBySIGINT runs the whole CLI: a SIGINT during
// `gonf apply <plan.jsonl>` cancels its signal context and stops the command.
func TestCLIApplyInterruptedBySIGINT(t *testing.T) {
	root := t.TempDir()
	started, marker := filepath.Join(root, "started"), filepath.Join(root, "after")
	path := writePlanFile(t, root, sleepThenTouchOps(started, marker))
	signalWhenStarted(t, syscall.SIGINT, started)

	start := time.Now()
	code, stderr := runGonf(t, "apply", path)
	requireInterrupted(t, code, time.Since(start), stderr, "apply: interrupted: ", marker)
}

// TestCLITaskInterruptedBySIGTERM: a SIGTERM during a local `gonf <task>`
// run stops the task's in-process command and fails the run as interrupted.
func TestCLITaskInterruptedBySIGTERM(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		api.ResetTasks()
		resource.ResetRepository()
	})
	root := t.TempDir()
	started, marker := filepath.Join(root, "started"), filepath.Join(root, "after")
	api.Task("cli_cancel_sleep", "sleep then touch", func() {
		sleep := api.Command("sh", startedScript(started))
		api.Command("touch", []string{marker}, options.DependsOn(sleep))
	})
	signalWhenStarted(t, syscall.SIGTERM, started)

	start := time.Now()
	code, stderr := runGonf(t, "cli_cancel_sleep")
	requireInterrupted(t, code, time.Since(start), stderr, "error: interrupted: ", marker)
}

// TestCLIApplyFailureNotInterrupted is the negative case: with a live ctx a
// failing command is reported as a plain apply failure, not an interrupt,
// and a succeeding plan still applies.
func TestCLIApplyFailureNotInterrupted(t *testing.T) {
	root := t.TempDir()
	failing := writePlanFile(t, root, []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "fail"},
		{Op: plan.KindCommand, Bin: "false", ID: "Command[false]"},
	})
	var code int
	stderr := testutil.CaptureStderr(t, func() { code = cliApply(context.Background(), []string{failing}) })
	if code != 1 || !strings.Contains(stderr, "apply: ") || strings.Contains(stderr, "interrupted") {
		t.Fatalf("exit %d, stderr %q; want 1 and a plain apply error", code, stderr)
	}

	marker := filepath.Join(root, "ok")
	okPlan := writeTouchPlan(t, root, "ok.jsonl", marker)
	_ = testutil.CaptureStderr(t, func() { code = cliApply(context.Background(), []string{okPlan}) })
	if code != 0 {
		t.Fatalf("live-ctx apply exit %d, want 0", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("live-ctx apply must run the command: %v", err)
	}
}

// TestCLIApplyCancelPipeStopsLongRunningCommand: task 3c2's core fix on the
// child side. "-cancel-pipe" makes cliApply treat stdin as an out-of-band
// cancel channel (api.runElevatedCmd closes it, instead of relying on
// sudo/doas to relay or withhold a signal correctly): closing it here
// (standing in for that close, with no OS signal and no ctx cancellation at
// all — context.Background()) must stop the long-running command through
// the very same "apply: interrupted: …" path as a signal-based cancel does,
// pinning that the pipe is only an alternate trigger, not a separate
// cancellation mechanism.
func TestCLIApplyCancelPipeStopsLongRunningCommand(t *testing.T) {
	root := t.TempDir()
	started, marker := filepath.Join(root, "started"), filepath.Join(root, "after")
	path := writePlanFile(t, root, sleepThenTouchOps(started, marker))

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	whenStarted(t, started, func() { _ = w.Close() })

	var code int
	start := time.Now()
	stderr := testutil.CaptureStderr(t, func() {
		code = cliApply(context.Background(), []string{"-cancel-pipe", path})
	})
	requireInterrupted(t, code, time.Since(start), stderr, "apply: interrupted: ", marker)
}

// TestCLIApplyFileIgnoresStdinWithoutCancelPipe is the regression guard for
// "-cancel-pipe" being opt-in: a plain `gonf apply <path>` (no flag) must
// never watch stdin. Without this, a cron/systemd invocation with stdin
// already at EOF (e.g. redirected from /dev/null, exactly like this test)
// would self-cancel the instant it started, which would be a severe
// regression for every non-interactive, non-elevated apply.
func TestCLIApplyFileIgnoresStdinWithoutCancelPipe(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ok")
	path := writeTouchPlan(t, root, "ok.jsonl", marker)

	oldStdin := os.Stdin
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = devNull
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = devNull.Close()
	})

	code := cliApply(context.Background(), []string{path})
	if code != 0 {
		t.Fatalf("exit %d, want 0 (stdin already at EOF must not affect a plain apply)", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("apply did not run: %v", err)
	}
}

// TestCLIApplyCancelPipeRefusedWithStdinPlan: "-cancel-pipe" dedicates
// stdin to the cancel channel, "-" dedicates it to the push payload; nothing
// sets both today (elevatedApplyArgv always passes a plan path), but the
// combination must be refused explicitly (exit 2) rather than silently
// letting one meaning win.
func TestCLIApplyCancelPipeRefusedWithStdinPlan(t *testing.T) {
	var code int
	stderr := testutil.CaptureStderr(t, func() {
		code = cliApply(context.Background(), []string{"-cancel-pipe", "-"})
	})
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr, "-cancel-pipe") || !strings.Contains(stderr, "stdin") {
		t.Fatalf("stderr %q, want a refusal mentioning -cancel-pipe and stdin", stderr)
	}
}

// TestInterruptedByCause pins that "interrupted" is decided by the error
// chain: a cancellation is one, a timeout or an unrelated failure is not,
// whatever the ctx state when the error surfaced.
func TestInterruptedByCause(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("plan: apply line 2: %w", context.Canceled), true},
		{fmt.Errorf("timed out after 5m0s: %w", context.DeadlineExceeded), false},
		{errors.New("false exited 1"), false},
	}
	for _, tc := range cases {
		if got := interrupted(tc.err); got != tc.want {
			t.Fatalf("interrupted(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
