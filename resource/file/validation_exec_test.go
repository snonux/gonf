package file

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	. "github.com/snonux/gonf/api/options"
	gexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/resource"
)

// These tests pin how the validator process is executed (task l62): it is
// bounded by the process-wide command timeout of internal/exec (the validator
// process itself is killed when it expires), a descendant holding the output
// pipe cannot block the call beyond validatorWaitDelay, and a bounded,
// sanitized copy of the combined stdout/stderr is appended to the error.

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
// background `sleep 30` inheriting the validator's stdout/stderr (so it holds
// the output pipe open) and records its pid. Gonf deliberately does not kill
// validator descendants, so the test kills it on cleanup to leave no process
// behind.
func lingeringChildScript(t *testing.T) string {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Cleanup(func() {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return `sleep 30 &
echo $! > "` + pidFile + `"
`
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
// call must end well before timeout + validatorWaitDelay (no pipe to drain).
func TestValidationTimeoutKillsValidator(t *testing.T) {
	resource.ResetRepository()
	setValidationCommandTimeout(t, time.Second)
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `echo "checking $1"
exec sleep 60`)

	elapsed, err := validateTimed(target, validator)
	if limit := time.Second + validatorWaitDelay/2; elapsed > limit {
		t.Fatalf("validation took %v, want at most %v", elapsed, limit)
	}
	prefix := "file " + target + ": validation by " + validator + " failed: timed out after 1s: context deadline exceeded: validator output: checking " + target + ".gonfvalidate"
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want prefix %q wrapping context.DeadlineExceeded", err, prefix)
	}
	assertNoLiveTarget(t, target)
}

// The timeout kill is SIGKILL, not a catchable signal: a validator that
// ignores SIGTERM still ends at the deadline, not validatorWaitDelay later.
func TestValidationTimeoutKillIsUncatchable(t *testing.T) {
	resource.ResetRepository()
	setValidationCommandTimeout(t, time.Second)
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `trap '' TERM
exec sleep 60`)

	elapsed, err := validateTimed(target, validator)
	if limit := time.Second + validatorWaitDelay/2; elapsed > limit {
		t.Fatalf("validation took %v, want at most %v (SIGKILL at the deadline)", elapsed, limit)
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a timeout", err)
	}
	assertNoLiveTarget(t, target)
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

// A timed-out validator whose child keeps the output pipe open still returns
// within timeout + validatorWaitDelay, as a timeout. The 1s timeout leaves
// the shell time to record the child's pid, so cleanup can kill it.
func TestValidationTimeoutNotBlockedByLingeringChild(t *testing.T) {
	resource.ResetRepository()
	setValidationCommandTimeout(t, time.Second)
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, lingeringChildScript(t)+`exec sleep 60`)

	elapsed, err := validateTimed(target, validator)
	if limit := time.Second + validatorWaitDelay + 2*time.Second; elapsed > limit {
		t.Fatalf("validation took %v, want at most %v", elapsed, limit)
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a timeout", err)
	}
	assertNoLiveTarget(t, target)
}

// The verdict is the validator's own exit status when it exits before the
// deadline, even if the deadline then expires while a lingering child still
// holds the output pipe (inside validatorWaitDelay): exit 0 stays a success
// and exit 3 stays an unwrappable exit error, never a timeout.
func TestValidationDeadlineDuringWaitDelayKeepsVerdict(t *testing.T) {
	for _, code := range []int{0, 3} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			resource.ResetRepository()
			setValidationCommandTimeout(t, time.Second)
			target := filepath.Join(privateValidationDir(t), "service.conf")
			validator := writeValidationScript(t, lingeringChildScript(t)+`sleep 0.3
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
// success; the call returns after validatorWaitDelay instead of waiting for
// the child.
func TestValidationSuccessNotBlockedByLingeringChild(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, lingeringChildScript(t)+`echo "syntax OK"
exit 0`)

	elapsed, err := validateTimed(target, validator)
	if err != nil {
		t.Fatalf("validated apply: %v", err)
	}
	if limit := validatorWaitDelay + 2*time.Second; elapsed > limit {
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
// followed by at most validatorOutputLimit bytes of output starting with
// wantStart and the truncation note naming total.
func assertCappedOutputErr(t *testing.T, err error, target, validator, wantStart string, total int) {
	t.Helper()
	prefix := "file " + target + ": validation by " + validator + " failed: exit status 2: validator output: "
	suffix := " [output truncated, " + strconv.Itoa(total) + " bytes in total]"
	if err == nil || !strings.HasPrefix(err.Error(), prefix+wantStart) || !strings.HasSuffix(err.Error(), suffix) {
		t.Fatalf("error = %.300q..., want prefix %q and suffix %q", err, prefix+wantStart, suffix)
	}
	if got := len(err.Error()) - len(prefix) - len(suffix); got > validatorOutputLimit {
		t.Fatalf("rendered output = %d bytes, want at most %d", got, validatorOutputLimit)
	}
}

// Huge validator output is drained completely (the validator is never
// blocked on a full pipe) but only validatorOutputLimit bytes reach the
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

// sanitizeValidatorOutput is the whole sanitizing pipeline in one call (a
// test helper): the sanitizeValidatorLines joined with validatorLineSeparator.
func sanitizeValidatorOutput(raw []byte) string {
	return strings.Join(sanitizeValidatorLines(raw), validatorLineSeparator)
}

// sanitizeValidatorOutput keeps printable text, folds lines (LF, CR, CRLF)
// into " | " separated segments, drops blank lines, and replaces control and
// format characters (terminal escapes, NUL, bidi overrides) and invalid UTF-8
// with '?', so validator output cannot forge log lines or terminal state.
func TestSanitizeValidatorOutput(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"\n \n\t\n", ""},
		{"ok\n", "ok"},
		{"a\r\nb\rc\n\nd", "a | b | c | d"},
		{"\x1b[31mred\x1b[0m", "?[31mred?[0m"},
		{"nul\x00byte\ttab", "nul?byte tab"},
		{"bidi\u202eevil", "bidi?evil"},
		{"bad\xff\xfeutf8", "bad?utf8"},
		{"unicode ü ok", "unicode ü ok"},
		{"line\u2028sep\u2029para", "line?sep?para"},
	}
	for _, tt := range tests {
		if got := sanitizeValidatorOutput([]byte(tt.in)); got != tt.want {
			t.Errorf("sanitizeValidatorOutput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// cappedOutput keeps at most its limit across several writes, always reports
// the full write as consumed, and names the total size once it truncated.
func TestCappedOutputKeepsPrefixAndCountsTotal(t *testing.T) {
	out := &cappedOutput{limit: 5}
	for _, chunk := range []string{"abc", "defg", "hij"} {
		if n, err := out.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil", chunk, n, err, len(chunk))
		}
	}
	if len(out.buf) > out.limit {
		t.Fatalf("kept %d raw bytes, want at most %d", len(out.buf), out.limit)
	}
	if got, want := out.String(), "abcde [output truncated, 10 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got := (&cappedOutput{limit: 5}).String(); got != "" {
		t.Fatalf("empty String() = %q, want empty", got)
	}
	short := &cappedOutput{limit: 5}
	_, _ = short.Write([]byte("abc\n"))
	if got := short.String(); got != "abc" {
		t.Fatalf("untruncated String() = %q, want %q", got, "abc")
	}
}

// Kept output that renders empty says so instead of an empty text before a
// truncation note; whitespace-only output with nothing dropped stays "".
func TestCappedOutputWithoutPrintableText(t *testing.T) {
	blank := &cappedOutput{limit: 5}
	_, _ = blank.Write([]byte("     x"))
	if got, want := blank.String(), "[no printable output; 6 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	spaces := &cappedOutput{limit: 5}
	_, _ = spaces.Write([]byte(" \n\t"))
	if got := spaces.String(); got != "" {
		t.Fatalf("whitespace String() = %q, want empty", got)
	}
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

// Sanitizing can grow the kept bytes; the rendered text is cut back to the
// limit and marked truncated even when no raw byte was dropped.
func TestCappedOutputCapsSanitizedText(t *testing.T) {
	out := &cappedOutput{limit: 6}
	_, _ = out.Write([]byte("a\nb\nc\n"))
	if got, want := out.String(), "a | b [output truncated, 6 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// A cut inside a separator does not leave it dangling.
	cut := &cappedOutput{limit: 4}
	_, _ = cut.Write([]byte("a\nb\n"))
	if got, want := cut.String(), "a [output truncated, 4 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// A cut inside a multi-byte rune backs off to the rune start: all 7 raw
	// bytes are kept, "ab | üü" renders to 9 bytes and byte 8 is the middle
	// of the second ü.
	multi := &cappedOutput{limit: 8}
	_, _ = multi.Write([]byte("ab\nüü"))
	got := multi.String()
	text := strings.TrimSuffix(got, " [output truncated, 7 bytes in total]")
	if text != "ab | ü" || !utf8.ValidString(got) || len(text) > multi.limit {
		t.Fatalf("String() = %q, want valid UTF-8 %q within %d bytes", got, "ab | ü", multi.limit)
	}
	// A cut that exposes a space inside a line drops it: "x\nab cd" renders
	// "x | ab cd" (9 bytes); at limit 7 the second line is cut to "ab ".
	inner := &cappedOutput{limit: 7}
	_, _ = inner.Write([]byte("x\nab cd"))
	if got, want := inner.String(), "x | ab [output truncated, 7 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// The validator's own " |" at the cut point is its text, not a separator
	// gonf inserted, so it is kept.
	own := &cappedOutput{limit: 4}
	_, _ = own.Write([]byte("ab |cd"))
	if got, want := own.String(), "ab | [output truncated, 6 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
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

// processState runs cmd to completion (after start, if given) and returns
// its real ProcessState.
func processState(t *testing.T, cmd *exec.Cmd, afterStart func(*os.Process)) *os.ProcessState {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if afterStart != nil {
		afterStart(cmd.Process)
	}
	_ = cmd.Wait()
	return cmd.ProcessState
}

// validatorTimedOut is only true when our kill was delivered and the
// validator did not exit on its own. The states are real: an exit 0 after a
// "successful" kill (the zombie window), a death by a signal we did not
// send, and a genuine kill.
func TestValidatorTimedOut(t *testing.T) {
	exited := processState(t, exec.Command("sh", "-c", "exit 0"), nil)
	foreign := processState(t, exec.Command("sh", "-c", "kill -USR1 $$"), nil)
	killed := processState(t, exec.Command("sleep", "60"), func(p *os.Process) {
		if err := p.Kill(); err != nil {
			t.Fatal(err)
		}
	})
	tests := []struct {
		name   string
		state  *os.ProcessState
		killed bool
		want   bool
	}{
		{"exited after kill (zombie)", exited, true, false},
		{"foreign signal", foreign, false, false},
		{"killed by us", killed, true, true},
		{"killed by someone else", killed, false, false},
		{"not started", nil, true, false},
	}
	for _, tt := range tests {
		if got := validatorTimedOut(tt.state, tt.killed); got != tt.want {
			t.Errorf("%s: validatorTimedOut = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// killForTimeout records the kill only when it was delivered.
func TestKillForTimeoutRecordsOnlyDeliveredKill(t *testing.T) {
	for _, tt := range []struct {
		killErr error
		want    bool
	}{{nil, true}, {os.ErrProcessDone, false}, {syscall.EPERM, false}} {
		var killed atomic.Bool
		err := killForTimeout(func() error { return tt.killErr }, &killed)
		if err != tt.killErr || killed.Load() != tt.want {
			t.Errorf("kill error %v: returned %v, recorded %v; want %v, %v", tt.killErr, err, killed.Load(), tt.killErr, tt.want)
		}
	}
}

// truncateUTF8 never splits a multi-byte rune.
func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abc", 2, "ab"},
		{"aü", 2, "a"},
		{"aü", 3, "aü"},
		{"ü", 1, ""},
	}
	for _, tt := range tests {
		if got := truncateUTF8(tt.in, tt.n); got != tt.want {
			t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
