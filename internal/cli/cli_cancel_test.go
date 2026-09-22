package cli

import (
	"bytes"
	"context"
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

// sleepThenTouchOps is a plan whose first op blocks for 30s and whose second
// op touches marker, so a test can tell whether the apply stopped after the
// canceled command.
func sleepThenTouchOps(marker string) []plan.Op {
	return []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "cancel"},
		{Op: plan.KindCommand, Bin: "sleep", Args: []string{"30"}, ID: "Command[sleep]"},
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
	if !strings.Contains(stderr, wantPrefix) || !strings.Contains(stderr, "canceled") {
		t.Fatalf("stderr %q lacks %q and the cancellation", stderr, wantPrefix)
	}
	requireNotTouched(t, marker)
}

// cancelSoon returns a ctx canceled 100ms from now (and at test cleanup).
func cancelSoon(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(100*time.Millisecond, cancel)
	return ctx
}

// TestCLIApplyFileCanceledByContext: canceling the CLI context while
// `gonf apply <plan.jsonl>` runs a long command kills it and fails the apply.
func TestCLIApplyFileCanceledByContext(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "after")
	path := writePlanFile(t, root, sleepThenTouchOps(marker))
	ctx := cancelSoon(t)

	var code int
	start := time.Now()
	stderr := testutil.CaptureStderr(t, func() { code = cliApply(ctx, []string{path}) })
	requireInterrupted(t, code, time.Since(start), stderr, "apply: interrupted (context canceled)", marker)
}

// TestCLIApplyStdinCanceledByContext: the receiving end of a push (`gonf
// apply -`) is canceled the same way.
func TestCLIApplyStdinCanceledByContext(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "after")
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, sleepThenTouchOps(marker), nil); err != nil {
		t.Fatal(err)
	}
	feedStdin(t, buf.Bytes())
	ctx := cancelSoon(t)

	var code int
	start := time.Now()
	stderr := testutil.CaptureStderr(t, func() { code = cliApply(ctx, []string{"-"}) })
	requireInterrupted(t, code, time.Since(start), stderr, "apply: interrupted (context canceled)", marker)
}

// guardSignal subscribes the test to sig for its duration, so a signal sent
// to the test process can never fall back to its default action (killing
// the test binary) even if CLI() already returned and stopped its own
// NotifyContext.
func guardSignal(t *testing.T, sig os.Signal) {
	t.Helper()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sig)
	t.Cleanup(func() { signal.Stop(ch) })
}

// signalSoon sends sig to this process after 300ms, while CLI() runs.
func signalSoon(t *testing.T, sig syscall.Signal) {
	t.Helper()
	guardSignal(t, sig)
	timer := time.AfterFunc(300*time.Millisecond, func() { _ = syscall.Kill(os.Getpid(), sig) })
	t.Cleanup(func() { timer.Stop() })
}

// TestCLIApplyInterruptedBySIGINT runs the whole CLI: a SIGINT during
// `gonf apply <plan.jsonl>` cancels its signal context and kills the command.
func TestCLIApplyInterruptedBySIGINT(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "after")
	path := writePlanFile(t, root, sleepThenTouchOps(marker))
	signalSoon(t, syscall.SIGINT)

	start := time.Now()
	code, stderr := runGonf(t, "apply", path)
	requireInterrupted(t, code, time.Since(start), stderr, "apply: interrupted (context canceled)", marker)
}

// TestCLITaskInterruptedBySIGTERM: a SIGTERM during a local `gonf <task>`
// run kills the task's in-process command and fails the run as interrupted.
func TestCLITaskInterruptedBySIGTERM(t *testing.T) {
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		api.ResetTasks()
		resource.ResetRepository()
	})
	marker := filepath.Join(t.TempDir(), "after")
	api.Task("cli_cancel_sleep", "sleep then touch", func() {
		sleep := api.Command("sleep", []string{"30"})
		api.Command("touch", []string{marker}, options.DependsOn(sleep))
	})
	signalSoon(t, syscall.SIGTERM)

	start := time.Now()
	code, stderr := runGonf(t, "cli_cancel_sleep")
	requireInterrupted(t, code, time.Since(start), stderr, "error: interrupted (context canceled)", marker)
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
