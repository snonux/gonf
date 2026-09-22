// Package validator runs configuration validators: one argv command (never a
// shell) bounded by the process-wide command timeout of internal/exec
// (api.SetCommandTimeout, CLI -cmd-timeout), which kills it together with
// the processes it started, with stdin from /dev/null and a bounded,
// sanitized copy of its combined output appended to the error. It is shared
// by the File resource's WithValidation and the ConfigSet resource's
// WithSetValidation, so both bound and report their validators identically.
package validator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	gexec "github.com/snonux/gonf/internal/exec"
)

// Run is RunIn in gonf's own working directory.
func Run(bin string, args []string) error {
	return RunIn("", bin, args)
}

const (
	// OutputLimit bounds the validator output that reaches an
	// error: at most this many raw bytes are kept, and the sanitized text
	// rendered from them is cut to this many bytes again (sanitizing can
	// grow it, e.g. "\n" becomes " | "). Output beyond the raw limit is
	// still read (so the validator never blocks on a full pipe) but only
	// counted.
	OutputLimit = 4096
	// WaitDelay bounds how long Wait keeps waiting for the output
	// pipe after the validator exited or was killed. A timeout also kills
	// the validator's descendants (killTree), but one it cannot reach (see
	// there) or one left behind by a validator that exited on its own, that
	// inherited its stdout or stderr, could otherwise hold the pipe, and so
	// the apply, open forever.
	WaitDelay = 2 * time.Second
)

// RunIn runs one validator (argv only, never a shell) in the working
// directory dir ("" keeps gonf's own) and returns nil when it exits 0. It is
// the shared executor of every validator (File's WithValidation and
// ConfigSet's WithSetValidation) and deliberately has no path prefix in its
// errors, so each caller wraps it with its own.
//
// It is bounded like every backend command by internal/exec's process-wide
// default timeout (api.SetCommandTimeout, CLI -cmd-timeout, 5m by default),
// but does not use exec.RunWith because that keeps unbounded output. The
// validator:
//   - gets stdin from /dev/null, so it never reads gonf's plan stream;
//   - stays in gonf's process group, so signals sent to that group (a
//     terminal's Ctrl-C, hangup and Ctrl-Z, or a SIGKILL of the whole group)
//     reach it exactly as they reach gonf;
//   - is killed together with its descendants (SIGKILL, see killTree in
//     proctree.go) when the timeout expires. A kill that fails (EPERM, e.g.
//     a validator run through sudo/doas by a non-root gonf) cannot bound it:
//     Wait then lasts until it exits, though exec closes the output pipe
//     WaitDelay after the failed kill, so its next write may end it with
//     SIGPIPE ("signal: broken pipe"). Descendants are killed on timeout
//     only; one that survives (it escaped killTree, or the validator exited
//     on its own) and still holds the output pipe is cut off after
//     WaitDelay, after which RunIn returns without waiting for it. The
//     verdict is the validator's own exit status: a deadline expiring while
//     only such a descendant is left does not turn it into a timeout;
//   - has its stdout and stderr captured together into one bounded buffer.
//
// A failure is the exit error, the timeout error or the start error, followed
// by ": validator output: <sanitized output>" when it printed anything. Only
// what the validator prints is included; gonf never adds the candidate's
// content, but a validator that echoes its input will leak it.
func RunIn(dir, bin string, args []string) error {
	timeout := gexec.DefaultTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output := &cappedOutput{limit: OutputLimit}
	// Stdin stays nil (/dev/null). Stdout and stderr share one writer, which
	// exec turns into one pipe, so their relative order is preserved.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Dir = dir
	cmd.WaitDelay = WaitDelay
	// exec calls Cancel at most once, from its context watcher, and only if
	// the deadline wins the race against Wait reaping the validator; a
	// deadline expiring later, while Wait merely drains a pipe held by a
	// lingering descendant, never reaches it. When reap and deadline
	// coincide, Cancel may still run just after the reap: killTree then
	// returns os.ErrProcessDone and nothing is recorded. It may also run
	// just before the reap, on a validator that already exited (a zombie):
	// killTree then kills the descendants still in its tree, the kill of the
	// zombie itself succeeds without effect and the flag is set anyway. The
	// flag therefore only says "we signalled it"; validatorTimedOut decides.
	var killedByTimeout atomic.Bool
	cmd.Cancel = func() error {
		return killForTimeout(func() error { return killTree(cmd.Process) }, &killedByTimeout)
	}
	err := cmd.Run()
	return withValidatorOutput(validatorRunError(cmd.ProcessState, killedByTimeout.Load(), timeout, err), output)
}

// killForTimeout is the validator's exec.Cmd.Cancel: it runs kill (killTree
// in RunIn, which also kills the descendants) and records in killed that the
// validator itself was killed, but only when kill reports that kill as
// delivered (a failed kill, e.g. EPERM or os.ErrProcessDone, leaves the
// process to its own fate and must not turn its verdict into a timeout).
func killForTimeout(kill func() error, killed *atomic.Bool) error {
	err := kill()
	if err == nil {
		killed.Store(true)
	}
	return err
}

// validatorTimedOut reports whether the timeout decided the validator's fate:
// our Cancel delivered the kill (killed) and the validator did not exit on
// its own. The Exited() check is the guard for the zombie window described
// in RunIn: a validator that exited, with any status, before
// being reaped keeps its own verdict even though the kill "succeeded". A
// validator that died of a signal nobody on our side sent (e.g. SIGSEGV) is
// not a timeout either, because killed is false.
func validatorTimedOut(state *os.ProcessState, killed bool) bool {
	return killed && state != nil && !state.Exited()
}

// validatorRunError maps cmd.Run's result to the validator's verdict:
//   - a timeout (see validatorTimedOut) is an error wrapping
//     context.DeadlineExceeded;
//   - otherwise a validator that exited 0 is a success, whatever Run said
//     about it: exec.ErrWaitDelay (a descendant held the output pipe past
//     WaitDelay) or a deadline that expired after the exit;
//   - anything else (an *exec.ExitError, including death by a signal we did
//     not send, or a start failure, where state is nil) is returned
//     unchanged.
func validatorRunError(state *os.ProcessState, killed bool, timeout time.Duration, err error) error {
	switch {
	case validatorTimedOut(state, killed):
		return fmt.Errorf("timed out after %v: %w", timeout, context.DeadlineExceeded)
	case err == nil, state != nil && state.Success():
		return nil
	}
	return err
}

// withValidatorOutput appends the captured output to a non-nil err, keeping
// err itself wrapped (e.g. for its exit code).
func withValidatorOutput(err error, output *cappedOutput) error {
	if err == nil {
		return nil
	}
	if text := output.String(); text != "" {
		return fmt.Errorf("%w: validator output: %s", err, text)
	}
	return err
}

// cappedOutput is an io.Writer that keeps the first limit bytes written to
// it and counts all of them. Write never fails, so the writer keeps
// draining. It is safe for concurrent writers.
type cappedOutput struct {
	mu    sync.Mutex
	limit int
	buf   []byte
	total int64
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keep := min(len(p), max(c.limit-len(c.buf), 0))
	c.buf = append(c.buf, p[:keep]...)
	c.total += int64(len(p))
	return len(p), nil
}

// String returns the sanitized kept output, cut to at most limit bytes,
// followed by a truncation note naming the total output size when anything
// was dropped, either raw or after sanitizing. Output that renders empty is
// "" when nothing was dropped (the validator printed only whitespace), and
// a "[no printable output; N bytes in total]" note otherwise, so the error
// never shows an empty text in front of a truncation note.
func (c *cappedOutput) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	text, cut := joinValidatorLines(sanitizeValidatorLines(c.buf), c.limit)
	truncated := cut || c.total > int64(len(c.buf))
	switch {
	case !truncated:
		return text
	case text == "":
		return fmt.Sprintf("[no printable output; %d bytes in total]", c.total)
	}
	return text + fmt.Sprintf(" [output truncated, %d bytes in total]", c.total)
}

// validatorLineSeparator joins sanitized validator output lines.
const validatorLineSeparator = " | "

// joinValidatorLines joins lines with validatorLineSeparator into at most
// limit bytes and reports whether it had to cut. A separator is only written
// together with (part of) the line after it, so a cut never leaves one
// dangling, while the validator's own text (even one ending in " |") is kept
// as far as it fits. A partly kept line is cut on a UTF-8 boundary and loses
// trailing spaces the cut exposed (e.g. "ab cd" cut to "ab "). Precondition:
// no line is empty or ends in a space, as sanitizeValidatorLines guarantees;
// otherwise a separator could be written with nothing after it.
func joinValidatorLines(lines []string, limit int) (string, bool) {
	var b strings.Builder
	for i, line := range lines {
		sep := ""
		if i > 0 {
			sep = validatorLineSeparator
		}
		room := limit - b.Len() - len(sep)
		if len(line) <= room {
			b.WriteString(sep)
			b.WriteString(line)
			continue
		}
		if part := strings.TrimRight(truncateUTF8(line, max(room, 0)), " "); part != "" {
			b.WriteString(sep)
			b.WriteString(part)
		}
		return b.String(), true
	}
	return b.String(), false
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune. s must be
// valid UTF-8 (its caller joinValidatorLines passes lines produced by
// sanitizeValidatorLines, which guarantees that).
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// sanitizeValidatorLines splits validator output into lines (at LF or CR),
// trims them and drops blank ones; tabs become spaces; control and format
// characters (terminal escapes, NUL, bidi overrides), the Unicode line and
// paragraph separators U+2028/U+2029 and invalid UTF-8 (e.g. a rune the
// output cap cut in half) become '?'. This stops output from forging log
// lines or terminal state.
func sanitizeValidatorLines(raw []byte) []string {
	text := strings.ToValidUTF8(string(raw), "?")
	lines := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' })
	kept := lines[:0]
	for _, line := range lines {
		if line = strings.TrimSpace(strings.Map(sanitizeValidatorRune, line)); line != "" {
			kept = append(kept, line)
		}
	}
	return kept
}

// sanitizeValidatorRune maps a tab to a space and any other control or
// format character, or a line/paragraph separator (U+2028/U+2029), to '?'.
func sanitizeValidatorRune(r rune) rune {
	switch {
	case r == '\t':
		return ' '
	case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp):
		return '?'
	}
	return r
}
