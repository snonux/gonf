package cli

import (
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// secondSignalHelperEnv makes the test binary act as the helper process of
// TestSignalContextSecondSignalForcesExit instead of running the test.
const secondSignalHelperEnv = "GONF_SECOND_SIGNAL_HELPER"

// runSecondSignalHelper is the helper process: the first SIGINT only
// cancels signalContext's ctx, the second must get the default action and
// kill the process before it reaches os.Exit(0).
func runSecondSignalHelper() {
	ctx, stop := signalContext()
	defer stop()
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	<-ctx.Done()
	// context.AfterFunc runs stop in its own goroutine; give it time to
	// remove the handler before the second signal.
	time.Sleep(500 * time.Millisecond)
	fmt.Println("first-signal-handled")
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	time.Sleep(5 * time.Second)
	os.Exit(0)
}

// TestSignalContextSecondSignalForcesExit: the first SIGINT cancels the CLI
// context (the process survives it), the second one terminates the process
// with the default action, so an operator can force-exit a gonf waiting for
// a graceful stop.
func TestSignalContextSecondSignalForcesExit(t *testing.T) {
	if os.Getenv(secondSignalHelperEnv) == "1" {
		runSecondSignalHelper()
		return
	}
	cmd := osexec.Command(os.Args[0], "-test.run=^TestSignalContextSecondSignalForcesExit$")
	cmd.Env = append(os.Environ(), secondSignalHelperEnv+"=1")
	out, err := cmd.Output()
	if !strings.Contains(string(out), "first-signal-handled") {
		t.Fatalf("helper did not survive the first SIGINT: out %q, err %v", out, err)
	}
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper err = %v, want it killed by the second SIGINT", err)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("helper exit status %v, want killed by SIGINT", exitErr)
	}
}
