package validator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	gexec "github.com/snonux/gonf/internal/exec"
)

// These tests pin that a validator runs in its own process group (task
// b82): a timeout kills everything the validator started in that group, and
// a terminating signal gonf receives still reaches the validator.

// setTimeout sets the process-wide command timeout for one test.
func setTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := gexec.DefaultTimeout()
	t.Cleanup(func() { gexec.SetDefaultTimeout(prev) })
	gexec.SetDefaultTimeout(d)
}

// readPid reads the pid a validator script wrote to path, failing the test
// when it is missing. It also registers a cleanup that SIGKILLs the pid, so
// a regression never leaves the process behind.
func readPid(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("validator did not record its child's pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad child pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	return pid
}

// waitGone polls until pid no longer exists (the killed child was reaped by
// whichever process it was reparented to) or the deadline passes.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("validator child %d still exists after the timeout", pid)
}

// A timed-out wrapper script is killed together with the long-running child
// it waits for: the child is gone afterwards and, since no survivor holds
// the output pipe, RunIn returns right at the deadline instead of WaitDelay
// later.
func TestRunInTimeoutKillsProcessGroup(t *testing.T) {
	setTimeout(t, 500*time.Millisecond)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := `sleep 30 &
echo $! > "` + pidFile + `"
wait`
	start := time.Now()
	err := RunIn("", "sh", []string{"-c", script})
	elapsed := time.Since(start)
	pid := readPid(t, pidFile)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if limit := 500*time.Millisecond + WaitDelay/2; elapsed > limit {
		t.Fatalf("RunIn took %v, want at most %v (no child left holding the pipe)", elapsed, limit)
	}
	waitGone(t, pid)
}

// Validators that finish before the deadline keep their own verdict and
// return promptly: joining their own process group changes neither a
// success nor a failure.
func TestRunInOwnGroupKeepsVerdict(t *testing.T) {
	setTimeout(t, time.Minute)
	tests := []struct {
		name   string
		script string
		code   int
	}{
		{"success", "echo ok; exit 0", 0},
		{"failure", "echo bad >&2; exit 3", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			err := RunIn("", "sh", []string{"-c", tt.script})
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("RunIn took %v, want a prompt return", elapsed)
			}
			if tt.code == 0 {
				if err != nil {
					t.Fatalf("err = %v, want success", err)
				}
				return
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tt.code || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want exit status %d and no timeout", err, tt.code)
			}
		})
	}
}

// The validator really leads its own process group.
func TestRunInOwnGroupSetsProcessGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", `ps -o pgid= -p $$ | tr -d ' '; echo $$`)
	var out strings.Builder
	cmd.Stdout = &out
	if err := runInOwnGroup(cmd); err != nil {
		t.Fatalf("runInOwnGroup: %v", err)
	}
	fields := strings.Fields(out.String())
	if len(fields) != 2 || fields[0] != fields[1] {
		t.Fatalf("pgid and pid = %q, want the validator to lead its own group", fields)
	}
}

// relay forwards the first received signal to the validator's group and
// re-raises it on gonf; driven through its channel, so no real signal hits
// the test binary.
func TestSignalForwarderRelaysToGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	reraised := make(chan os.Signal, 1)
	f := &signalForwarder{
		signals:  make(chan os.Signal, 1),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
		reraise:  func(sig os.Signal) { reraised <- sig },
	}
	f.start(cmd.Process)
	f.signals <- syscall.SIGTERM
	err := cmd.Wait()
	f.stop()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Fatalf("validator ended with %v, want death by the forwarded SIGTERM", err)
	}
	select {
	case sig := <-reraised:
		if sig != syscall.SIGTERM {
			t.Fatalf("re-raised %v, want SIGTERM", sig)
		}
	default:
		t.Fatal("the forwarded signal was not re-raised on gonf")
	}
}

// End to end, a SIGINT sent to gonf (as Ctrl-C does) while a validator runs
// reaches the validator in its own group, and gonf itself sees the signal
// twice: the original and the forwarder's re-raise. The test holds its own
// SIGINT registration throughout, so neither kills the test binary, and
// stops it only after both arrived.
func TestRunInForwardsInterruptToValidator(t *testing.T) {
	guard := make(chan os.Signal, 2)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)
	setTimeout(t, time.Minute)
	ready := filepath.Join(t.TempDir(), "ready")
	result := make(chan error, 1)
	go func() {
		result <- RunIn("", "sh", []string{"-c", `touch "` + ready + `"; exec sleep 30`})
	}()
	waitForFile(t, ready)
	if err := unix.Kill(os.Getpid(), unix.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || err.Error() != "signal: interrupt" {
			t.Fatalf("err = %v, want the validator killed by the forwarded SIGINT", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the validator did not receive the forwarded SIGINT")
	}
	for i := range 2 {
		select {
		case <-guard:
		case <-time.After(5 * time.Second):
			t.Fatalf("gonf saw SIGINT %d times, want the original and the re-raise", i)
		}
	}
}

// waitForFile polls until path exists or fails the test after 5 seconds.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
}
