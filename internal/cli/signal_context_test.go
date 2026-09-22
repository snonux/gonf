package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/snonux/gonf/plan"
)

// helperEnv selects which helper process the test binary acts as (see
// runSignalHelper) instead of running tests.
const helperEnv = "GONF_SIGNAL_HELPER"

// TestMain-free dispatch: each subprocess test re-executes the test binary
// with -test.run naming itself and helperEnv set; the test then runs the
// helper and exits instead of doing its parent-side checks.
func runSignalHelper(t *testing.T, name string, helper func()) bool {
	t.Helper()
	if os.Getenv(helperEnv) != name {
		return false
	}
	helper()
	os.Exit(0)
	return true
}

// helperCmd builds the command re-executing this test binary as helper name
// for the test testName; wrap, when set, prefixes a shell wrapper argv.
func helperCmd(testName, name string, wrap ...string) *osexec.Cmd {
	argv := append(wrap, os.Args[0], "-test.run=^"+testName+"$")
	cmd := osexec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), helperEnv+"="+name)
	return cmd
}

// signaledBy reports whether err is the exit of a process killed by sig.
func signaledBy(err error, sig syscall.Signal) bool {
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == sig
}

// secondSignalHelper: the first SIGINT only cancels the outer process's
// signal context, the second must get the default action and kill the
// process before it returns.
func secondSignalHelper() {
	ctx, stop := signalContext(true)
	defer stop()
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	<-ctx.Done()
	// context.AfterFunc runs stop in its own goroutine; give it time to
	// remove the handler before the second signal.
	time.Sleep(500 * time.Millisecond)
	fmt.Println("first-signal-handled")
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	time.Sleep(5 * time.Second)
}

// TestSignalContextSecondSignalForcesExit: in the outer process the first
// SIGINT cancels the CLI context (the process survives it) and the second
// terminates it with the default action, so an operator can force-exit a
// gonf waiting for a graceful stop.
func TestSignalContextSecondSignalForcesExit(t *testing.T) {
	if runSignalHelper(t, "second", secondSignalHelper) {
		return
	}
	out, err := helperCmd("TestSignalContextSecondSignalForcesExit", "second").Output()
	if !strings.Contains(string(out), "first-signal-handled") {
		t.Fatalf("helper did not survive the first SIGINT: out %q, err %v", out, err)
	}
	if !signaledBy(err, syscall.SIGINT) {
		t.Fatalf("helper err = %v, want it killed by the second SIGINT", err)
	}
}

// hangupHelper sends itself SIGHUP under signalContext and reports whether
// its context was canceled.
func hangupHelper() {
	ctx, stop := signalContext(true)
	defer stop()
	_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
	select {
	case <-ctx.Done():
		fmt.Println("canceled")
	case <-time.After(500 * time.Millisecond):
		fmt.Println("survived")
	}
}

// TestSignalContextKeepsIgnoredSIGHUP: a gonf started with SIGHUP ignored
// (nohup) keeps it ignored and keeps running after a hangup, while a normal
// one stops on it.
func TestSignalContextKeepsIgnoredSIGHUP(t *testing.T) {
	if runSignalHelper(t, "hangup", hangupHelper) {
		return
	}
	cases := []struct {
		name string
		wrap []string
		want string
	}{
		{"SIGHUP ignored (nohup)", []string{"sh", "-c", `trap '' HUP; exec "$@"`, "sh"}, "survived"},
		{"SIGHUP default", nil, "canceled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := helperCmd("TestSignalContextKeepsIgnoredSIGHUP", "hangup", tc.wrap...).Output()
			if err != nil || strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("helper out %q, err %v; want %q and exit 0", out, err, tc.want)
			}
		})
	}
}

// applyChildHelper runs `gonf apply -` on the push payload from stdin.
func applyChildHelper() {
	os.Args = []string{"gonf", "apply", "-"}
	os.Exit(CLI())
}

// TestApplyChildSurvivesRepeatedSignals: a `gonf apply` process (elevated
// child, push destination) gets two signals (sudo relays the terminal's
// SIGINT and the outer gonf's SIGTERM). The second must not kill it: it
// stops gracefully, exits 1 as interrupted and its deferred cleanup (the
// apply run dir) is done.
func TestApplyChildSurvivesRepeatedSignals(t *testing.T) {
	if runSignalHelper(t, "apply", applyChildHelper) {
		return
	}
	tmp, root := t.TempDir(), t.TempDir()
	started, marker := filepath.Join(root, "started"), filepath.Join(root, "after")
	// The first op takes a second to stop after its SIGTERM (a package
	// manager's clean shutdown), so the second signal arrives mid-stop.
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "two-signals"},
		{Op: plan.KindCommand, Bin: "sh", ID: "Command[slow-stop]", Args: []string{"-c",
			"touch '" + started + "'; trap 'sleep 1; exit 1' TERM; while :; do sleep 0.1; done"}},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[touch]"},
	}
	var payload bytes.Buffer
	if err := plan.EncodePush(&payload, ops, nil); err != nil {
		t.Fatal(err)
	}
	cmd := helperCmd("TestApplyChildSurvivesRepeatedSignals", "apply")
	cmd.Env = append(cmd.Env, "TMPDIR="+tmp)
	cmd.Stdin = &payload
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, started)
	_ = cmd.Process.Signal(syscall.SIGINT)
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Signal(syscall.SIGTERM)

	err := cmd.Wait()
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("helper err = %v, want a clean exit 1; stderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "apply: interrupted: ") {
		t.Fatalf("stderr lacks the interrupted report:\n%s", stderr.String())
	}
	requireNotTouched(t, marker)
	requireNoRunDirs(t, filepath.Join(tmp, "gonf-apply"))
}

// TestForceExitOnRepeat pins which invocations may be force-exited by a
// second signal: every one except `gonf apply`.
func TestForceExitOnRepeat(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"apply", "-"}, false},
		{[]string{"apply", "plan.jsonl"}, false},
		{[]string{"my_task"}, true},
		{[]string{"push", "host"}, true},
		{nil, true},
	}
	for _, tc := range cases {
		if got := forceExitOnRepeat(cliOptions{args: tc.args}); got != tc.want {
			t.Fatalf("forceExitOnRepeat(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// waitForFile polls for path for up to promptReturn.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(promptReturn)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// requireNoRunDirs fails if any apply run dir ("run-*") is left below the
// staging tree root.
func requireNoRunDirs(t *testing.T, root string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(root, "*", "run-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("apply run dirs left behind: %v", left)
	}
}
