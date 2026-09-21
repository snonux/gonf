package validator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gexec "github.com/snonux/gonf/internal/exec"
)

// RunIn runs the validator in the given working directory; Run keeps gonf's.
func TestRunInUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunIn(dir, "sh", []string{"-c", "test -f marker"}); err != nil {
		t.Fatalf("RunIn in %s: %v", dir, err)
	}
	if err := Run("sh", []string{"-c", "test -f marker"}); err == nil {
		t.Fatal("Run must not use RunIn's directory")
	}
}

// A failure wraps the exit error and appends the sanitized output; a success
// ignores output; stdin is /dev/null (a validator reading stdin sees EOF).
func TestRunInReportsOutputAndExitStatus(t *testing.T) {
	err := RunIn("", "sh", []string{"-c", "echo 'bad\tline'; echo second >&2; exit 4"})
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 4 {
		t.Fatalf("err = %v, want the exit status 4 wrapped", err)
	}
	if !strings.HasSuffix(err.Error(), ": validator output: bad line | second") {
		t.Fatalf("err = %q, want the sanitized combined output", err)
	}
	if err := RunIn("", "sh", []string{"-c", "echo noise; cat >/dev/null"}); err != nil {
		t.Fatalf("success with output and stdin read: %v", err)
	}
}

// The process-wide command timeout bounds the validator.
func TestRunInIsBoundedByCommandTimeout(t *testing.T) {
	prev := gexec.DefaultTimeout()
	t.Cleanup(func() { gexec.SetDefaultTimeout(prev) })
	gexec.SetDefaultTimeout(300 * time.Millisecond)
	start := time.Now()
	err := RunIn("", "sleep", []string{"30"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond+WaitDelay {
		t.Fatalf("RunIn took %v, want it bounded by the timeout", elapsed)
	}
}
