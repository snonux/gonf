package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/privilege"
)

// writeFakeElevatedChild writes a script standing in for the elevated
// process runElevatedCmd spawns (in real use: sudo/doas, or the gonf binary
// they wrap). It traps SIGTERM (logging "sigterm" to the log file named by
// its first argument, without exiting — mirroring an elevated gonf apply
// whose own graceful-shutdown path does not exit merely because it saw a
// signal) and then blocks reading stdin: on EOF it logs "eof", sleeps
// briefly (standing in for the "finish the in-flight backend command"
// graceful-shutdown work an elevated apply's own context cancellation
// triggers) and logs "done" before exiting 0. Real doas/sudo are never
// invoked; only this script is (directly, as argv[0], since runElevatedCmd
// itself does not care whether argv[0] is a wrapper or the wrapped command).
func writeFakeElevatedChild(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-elevated-child.sh")
	content := "#!/bin/bash\n" +
		"trap 'echo sigterm >> \"$1\"' TERM\n" +
		"cat >/dev/null\n" +
		"echo eof >> \"$1\"\n" +
		"sleep 0.2\n" +
		"echo done >> \"$1\"\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake elevated child: %v", err)
	}
	return script
}

// runCancelableElevatedCmd starts runElevatedCmd(ctx, mode, [script,
// logPath]) in a goroutine, waits for the child to be blocked on stdin (its
// trap is installed by the time "cat" starts, which happens almost
// immediately), cancels ctx, and returns once runElevatedCmd returns (or
// fails the test after a generous timeout, so a regression hangs the test
// instead of the whole suite).
func runCancelableElevatedCmd(t *testing.T, mode privilege.Mode, script, logPath string) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runElevatedCmd(ctx, mode, []string{script, logPath}) }()

	// Give the child time to install its trap and start blocking on stdin
	// before it is asked to stop; this is a real subprocess (not a fake), so
	// there is no test seam to synchronize on instead.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("runElevatedCmd did not return after ctx was canceled")
		return nil
	}
}

// TestRunElevatedCmdPipeCancelsDoasChildWithoutSignal is task 3c2's core
// fix, pinned for the doas mode: canceling ctx must close the cancel pipe
// (the child observes EOF and runs its full graceful-shutdown path, "eof"
// then "done" in the log) WITHOUT sending the wrapper a SIGTERM. Sending one
// would reach OpenDoas's own premature-kill logic (see runElevatedCmd's and
// elevatedCancelGrace's doc comments, backed by an empirical test against
// real opendoas 6.8.2: an unprivileged SIGTERM to it succeeds, but OpenDoas
// then kills the child ~2s later regardless of the child's own graceful
// shutdown), undoing the whole point of the pipe.
func TestRunElevatedCmdPipeCancelsDoasChildWithoutSignal(t *testing.T) {
	dir := t.TempDir()
	script := writeFakeElevatedChild(t)
	logPath := filepath.Join(dir, "events.log")

	err := runCancelableElevatedCmd(t, privilege.Doas, script, logPath)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("runElevatedCmd err = %v, want an error wrapping context.Canceled", err)
	}

	raw, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	events := string(raw)
	if !strings.Contains(events, "eof") {
		t.Fatalf("child never saw EOF on stdin (cancel pipe not closed): log = %q", events)
	}
	if !strings.Contains(events, "done") {
		t.Fatalf("child did not finish its graceful-shutdown work before exiting: log = %q", events)
	}
	if strings.Contains(events, "sigterm") {
		t.Fatalf("doas wrapper must not be signalled: log = %q", events)
	}
}

// TestRunElevatedCmdSudoStillGetsSignalAndPipe pins the sudo mode: canceling
// ctx still closes the cancel pipe (same as doas) AND sends the wrapper an
// explicit SIGTERM, kept as harmless defense-in-depth because sudo relays
// signals to its child correctly (unlike OpenDoas). Both must fire; neither
// path is dropped by adding the other.
func TestRunElevatedCmdSudoStillGetsSignalAndPipe(t *testing.T) {
	dir := t.TempDir()
	script := writeFakeElevatedChild(t)
	logPath := filepath.Join(dir, "events.log")

	err := runCancelableElevatedCmd(t, privilege.Sudo, script, logPath)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("runElevatedCmd err = %v, want an error wrapping context.Canceled", err)
	}

	raw, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	events := string(raw)
	if !strings.Contains(events, "sigterm") {
		t.Fatalf("sudo wrapper was not signalled (defense-in-depth path dropped): log = %q", events)
	}
	if !strings.Contains(events, "eof") {
		t.Fatalf("child never saw EOF on stdin (cancel pipe not closed): log = %q", events)
	}
	if !strings.Contains(events, "done") {
		t.Fatalf("child did not finish its graceful-shutdown work before exiting: log = %q", events)
	}
}

// TestRunElevatedCmdCancelAfterChildExitsDoesNotPanic covers the "already
// exited by the time cancel fires" edge from task 3c2's self-review: a
// child that exits on its own (never reads stdin here) right as ctx is
// canceled must not make runElevatedCmd panic or hang, for either mode, and
// closing the cancel pipe twice (once from cmd.Cancel if it still fires,
// once from runElevatedCmd's own deferred cleanup) must be a no-op, not a
// crash — pinning that closeCancelPipe's sync.Once actually protects it.
func TestRunElevatedCmdCancelAfterChildExitsDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fast-exit.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fast-exit script: %v", err)
	}

	for _, mode := range []privilege.Mode{privilege.Sudo, privilege.Doas} {
		t.Run(mode.String(), func(t *testing.T) {
			for i := 0; i < 5; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- runElevatedCmd(ctx, mode, []string{script}) }()
				// Cancel immediately, racing the child's own near-instant
				// exit; either order must be handled without a panic.
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("runElevatedCmd did not return")
				}
			}
		})
	}
}
