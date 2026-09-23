package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	. "github.com/snonux/gonf/api/options"
	gexec "github.com/snonux/gonf/internal/exec"
	ivalidator "github.com/snonux/gonf/internal/validator"
	"github.com/snonux/gonf/resource"
)

// These tests pin how the validator process is executed (tasks l62, b82): it
// is bounded by the process-wide command timeout of internal/exec (the
// validator and its descendants are killed when it expires), a descendant
// holding the output pipe cannot block the call beyond validator.WaitDelay,
// and a bounded, sanitized copy of the combined stdout/stderr is appended to
// the error. internal/validator/proctree_test.go covers the descendant kill.

// lingeringChild is the background child a lingeringChildScript validator
// starts: pidFile is where the script records its pid, and gone is set once
// assertGone confirmed the process no longer exists, so cleanup never
// signals a pid that may since have been recycled.
type lingeringChild struct {
	pidFile string
	gone    bool
}

// setValidationCommandTimeout sets the process-wide command timeout the
// validator inherits (the knob behind api.SetCommandTimeout and -cmd-timeout)
// for one test and restores the previous value afterwards.
func setValidationCommandTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := gexec.DefaultTimeout()
	t.Cleanup(func() { gexec.SetDefaultTimeout(prev) })
	gexec.SetDefaultTimeout(d)
}

// assertNoLiveTarget checks a failed validation neither published the target
// nor left a candidate beside it.
func assertNoLiveTarget(t *testing.T, target string) {
	t.Helper()
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed validation published live target: %v", err)
	}
	if candidates, err := filepath.Glob(target + ".gonfvalidate*"); err != nil || len(candidates) != 0 {
		t.Fatalf("candidates after failed validation = %v, glob error = %v", candidates, err)
	}
}

// lingeringChildScript returns a validator script prefix that starts a
// background `sleep 60` inheriting the validator's stdout/stderr (so it holds
// the output pipe open) and records its pid, plus the handle to that child.
// Gonf kills validator descendants only on a timeout, so unless assertGone
// confirmed it gone the test kills it on cleanup to leave no process behind.
// The sleep is long (60s) so a regression that leaves the call waiting on the
// child instead of cutting it off is unmistakable against the load-tolerant
// bounds the callers check.
func lingeringChildScript(t *testing.T) (string, *lingeringChild) {
	t.Helper()
	child := &lingeringChild{pidFile: filepath.Join(t.TempDir(), "child.pid")}
	t.Cleanup(func() {
		if pid, err := child.pid(); err == nil && !child.gone {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return `sleep 60 &
echo $! > "` + child.pidFile + `"
`, child
}

// pid returns the child's recorded pid.
func (c *lingeringChild) pid() (int, error) {
	raw, err := os.ReadFile(c.pidFile)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err == nil && pid <= 0 {
		err = errors.New("invalid pid " + strconv.Itoa(pid))
	}
	return pid, err
}

// assertGone checks the child was killed: its process disappears (once
// reaped by whichever process it was reparented to) within 10 seconds.
func (c *lingeringChild) assertGone(t *testing.T) {
	t.Helper()
	pid, err := c.pid()
	if err != nil {
		t.Fatalf("lingering child pid: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			c.gone = true
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("lingering child %d survived the timeout kill", pid)
}

// validateTimed runs a validated Ensure and returns its duration and error.
func validateTimed(target, validator string) (time.Duration, error) {
	start := time.Now()
	err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath}))
	return time.Since(start), err
}

// A hanging validator is killed when the command timeout expires: the error
// names the timeout (and wraps context.DeadlineExceeded), carries what the
// validator printed, the live file is not touched and the candidate is gone.
// The timeout is 1s so the shell reliably prints before it is killed; the
// call must end well before it would if the kill regressed and Wait ran out
// the full sleep instead (60s). The bound (timeout + WaitDelay + generous
// slack) leaves room for a busy host without losing that separation.
func TestValidationTimeoutKillsValidator(t *testing.T) {
	resource.ResetRepository()
	setValidationCommandTimeout(t, time.Second)
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `echo "checking $1"
exec sleep 60`)

	elapsed, err := validateTimed(target, validator)
	if limit := time.Second + ivalidator.WaitDelay + 10*time.Second; elapsed > limit {
		t.Fatalf("validation took %v, want at most %v", elapsed, limit)
	}
	prefix := "file " + target + ": validation by " + validator + " failed: timed out after 1s: context deadline exceeded: validator output: checking " + target + ".gonfvalidate"
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want prefix %q wrapping context.DeadlineExceeded", err, prefix)
	}
	assertNoLiveTarget(t, target)
}

// The timeout kill is SIGKILL, never a catchable signal: killTree (task b82,
// proctree.go) SIGSTOPs the validator before it ever kills it, so a live
// process never gets the chance to run a SIGTERM handler at all. A duration
// bound cannot prove that on its own: a regression that sent a catchable
// SIGTERM instead would still have this validator exit well within the
// bound below (it traps and returns almost immediately), so the test would
// keep passing on timing alone -- which is exactly why an earlier version of
// this test (with a script that only ignored SIGTERM via an empty TERM
// trap) missed it. Instead the validator traps SIGTERM to write a marker
// file, and the assertion is that the marker is ABSENT afterward: proof no
// SIGTERM ever reached it, independent of timing entirely. The sleep runs
// backgrounded with an explicit `wait` (rather than a plain foreground
// `sleep 60`, or `exec`ing into it) because a shell only runs a queued trap
// once its current foreground job returns: `exec sleep 60` would also
// discard the trap outright (exec resets a caught signal's disposition to
// default), and a bare foreground `sleep 60` would leave the trap unrun
// until that sleep itself ends, defeating the point. The elapsed bound is
// kept too, as a sanity check that the kill still happens at the deadline
// and not ivalidator.WaitDelay later or after the full 60s sleep.
func TestValidationTimeoutKillIsUncatchable(t *testing.T) {
	resource.ResetRepository()
	setValidationCommandTimeout(t, time.Second)
	target := filepath.Join(privateValidationDir(t), "service.conf")
	mark := filepath.Join(t.TempDir(), "term-caught")
	validator := writeValidationScript(t, `trap 'echo x > "`+mark+`"' TERM
sleep 60 &
wait`)

	elapsed, err := validateTimed(target, validator)
	if limit := time.Second + ivalidator.WaitDelay + 10*time.Second; elapsed > limit {
		t.Fatalf("validation took %v, want at most %v (SIGKILL at the deadline)", elapsed, limit)
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a timeout", err)
	}
	assertNoLiveTarget(t, target)
	if _, statErr := os.Stat(mark); !os.IsNotExist(statErr) {
		t.Fatalf("TERM trap marker present: the kill delivered a catchable SIGTERM, want SIGKILL only")
	}
}

// The validator's stdin is /dev/null, never gonf's own stdin (which carries
// the plan stream when gonf applies a pushed plan): with data waiting on
// gonf's stdin, a validator that reads a line still sees EOF. Not parallel:
// it replaces os.Stdin.
func TestValidationStdinIsNotInherited(t *testing.T) {
	resource.ResetRepository()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := w.WriteString("plan stream line\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	prev := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = prev })

	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `if read -r line; then
  echo "read from stdin: $line"
  exit 1
fi
exit 0`)
	if err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath})); err != nil {
		t.Fatalf("validator saw gonf's stdin: %v", err)
	}
}

// A timed-out validator whose child keeps the output pipe open returns as a
// timeout: the timeout kill takes the child too, and even a child that
// escaped it could only delay the return to timeout + ivalidator.WaitDelay.
// The 1s timeout leaves the shell time to record the child's pid, which the
// test uses to check the child is gone (and cleanup to kill it should the
// descendant kill regress). The bound adds generous slack on top of
// timeout+WaitDelay for a busy host, well short of the lingering child's 60s
// sleep, which is what a regressed descendant kill would leave the call
// waiting on.
func TestValidationTimeoutNotBlockedByLingeringChild(t *testing.T) {
	resource.ResetRepository()
	setValidationCommandTimeout(t, time.Second)
	target := filepath.Join(privateValidationDir(t), "service.conf")
	script, child := lingeringChildScript(t)
	validator := writeValidationScript(t, script+`exec sleep 60`)

	elapsed, err := validateTimed(target, validator)
	if limit := time.Second + ivalidator.WaitDelay + 10*time.Second; elapsed > limit {
		t.Fatalf("validation took %v, want at most %v", elapsed, limit)
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a timeout", err)
	}
	assertNoLiveTarget(t, target)
	child.assertGone(t)
}

// The verdict is the validator's own exit status when it exits before the
// deadline, even if the deadline then expires while a lingering child still
// holds the output pipe (inside ivalidator.WaitDelay): exit 0 stays a success
// and exit 3 stays an unwrappable exit error, never a timeout.
func TestValidationDeadlineDuringWaitDelayKeepsVerdict(t *testing.T) {
	for _, code := range []int{0, 3} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			resource.ResetRepository()
			setValidationCommandTimeout(t, time.Second)
			target := filepath.Join(privateValidationDir(t), "service.conf")
			script, _ := lingeringChildScript(t)
			validator := writeValidationScript(t, script+`sleep 0.3
echo done
exit `+strconv.Itoa(code))

			elapsed, err := validateTimed(target, validator)
			if elapsed < time.Second {
				t.Fatalf("validation took %v, want the deadline to pass while the pipe drains", elapsed)
			}
			if code == 0 {
				wantValidationErr(t, err, "")
				return
			}
			wantValidationErr(t, err, "file "+target+": validation by "+validator+" failed: exit status 3: validator output: done")
			var exitErr interface{ ExitCode() int }
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != code {
				t.Fatalf("validator exit error not unwrappable from %v", err)
			}
			assertNoLiveTarget(t, target)
		})
	}
}

// A validator that exits 0 while a child it started keeps stdout open is a
// success; the call returns after ivalidator.WaitDelay instead of waiting for
// the child. The bound adds generous slack for a busy host, well short of
// the lingering child's 60s sleep that a regression (waiting for the child)
// would show up as.
func TestValidationSuccessNotBlockedByLingeringChild(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	script, _ := lingeringChildScript(t)
	validator := writeValidationScript(t, script+`echo "syntax OK"
exit 0`)

	elapsed, err := validateTimed(target, validator)
	if err != nil {
		t.Fatalf("validated apply: %v", err)
	}
	if limit := ivalidator.WaitDelay + 10*time.Second; elapsed > limit {
		t.Fatalf("validation took %v, want at most %v", elapsed, limit)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "candidate" {
		t.Fatalf("live content = %q, %v; want published candidate", got, err)
	}
}

// The validator's diagnostics (stdout and stderr, in the order written) are
// part of the error as " | " separated lines, and the exit status stays
// unwrappable.
func TestValidationFailureIncludesValidatorOutput(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `echo "service.conf:12: syntax error near 'x'" >&2
echo "1 error"
exit 1`)
	err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath}))
	wantValidationErr(t, err, "file "+target+": validation by "+validator+
		" failed: exit status 1: validator output: service.conf:12: syntax error near 'x' | 1 error")
	var exitErr interface{ ExitCode() int }
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("validator exit error not unwrappable from %v", err)
	}
	assertNoLiveTarget(t, target)
}

// assertCappedOutputErr checks err is the exit-status error of validator
// followed by at most ivalidator.OutputLimit bytes of output starting with
// wantStart and the truncation note naming total.
func assertCappedOutputErr(t *testing.T, err error, target, validator, wantStart string, total int) {
	t.Helper()
	prefix := "file " + target + ": validation by " + validator + " failed: exit status 2: validator output: "
	suffix := " [output truncated, " + strconv.Itoa(total) + " bytes in total]"
	if err == nil || !strings.HasPrefix(err.Error(), prefix+wantStart) || !strings.HasSuffix(err.Error(), suffix) {
		t.Fatalf("error = %.300q..., want prefix %q and suffix %q", err, prefix+wantStart, suffix)
	}
	if got := len(err.Error()) - len(prefix) - len(suffix); got > ivalidator.OutputLimit {
		t.Fatalf("rendered output = %d bytes, want at most %d", got, ivalidator.OutputLimit)
	}
}

// Huge validator output is drained completely (the validator is never
// blocked on a full pipe) but only ivalidator.OutputLimit bytes reach the
// error, followed by the total size.
func TestValidationFailureCapsHugeOutput(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `head -c 1000000 /dev/zero | tr '\0' 'x'
exit 2`)
	err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath}))
	assertCappedOutputErr(t, err, target, validator, "xxxx", 1000000)
}

// The cap also holds after sanitizing: 4096 raw bytes of short lines would
// render to about twice that with " | " separators.
func TestValidationFailureCapsSanitizedOutput(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `yes a | head -n 2048
exit 2`)
	err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath}))
	assertCappedOutputErr(t, err, target, validator, "a | a | a", 4096)
}

// Output of a successful validator is not an error and does not change the
// normal success path.
func TestValidationSuccessIgnoresValidatorOutput(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `echo "syntax OK"; echo "warning: deprecated" >&2`)
	if err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath})); err != nil {
		t.Fatalf("validated apply: %v", err)
	}
}

// A validator that cannot be started keeps the historic start-failure error.
func TestValidationStartFailureIsReported(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	missing := filepath.Join(t.TempDir(), "missing-validator")
	err := Ensure(target, WithContent("candidate"), WithValidation(missing, []string{CandidatePath}))
	want := "file " + target + ": validation by " + missing + " failed: fork/exec " + missing + ": no such file or directory"
	if err == nil || err.Error() != want || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want %q", err, want)
	}
	assertNoLiveTarget(t, target)
}

// End to end, a validator whose kept output is only whitespace reports that
// with the total size, never an empty "validator output:" text.
func TestValidationFailureWithoutPrintableOutput(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `head -c 5000 /dev/zero | tr '\0' ' '
echo x
exit 2`)
	err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath}))
	wantValidationErr(t, err, "file "+target+": validation by "+validator+
		" failed: exit status 2: validator output: [no printable output; 5002 bytes in total]")
}

// A validator killed by a signal nobody on gonf's side sent reports that
// signal, not a timeout, even with a timeout configured. SIGUSR1 is used
// because it terminates without a core dump (SIGSEGV would leave coredump
// records or sh.core files on every run).
func TestValidationSignalDeathIsNotTimeout(t *testing.T) {
	resource.ResetRepository()
	setValidationCommandTimeout(t, time.Minute)
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `kill -USR1 $$`)
	err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath}))
	want := "file " + target + ": validation by " + validator + " failed: signal: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want prefix %q and no timeout", err, want)
	}
	assertNoLiveTarget(t, target)
}
