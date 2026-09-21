package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	gexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/safepath"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// ensureValidatedFile validates content from a private, unique candidate
// before the normal single-file reconciliation can replace the live target.
// A candidate is created beside the target, so validators that resolve simple
// relative paths from the configuration's parent keep that context. This
// deliberately does not try to model chroots or multi-file include sets.
func (f *File) ensureValidatedFile(path string, content []byte) error {
	if !f.validationSet || resource.DryRun() {
		return f.ensureFile(path, content)
	}
	if err := f.validateCandidate(path, content); err != nil {
		return err
	}
	return f.ensureFile(path, content)
}

// validateCandidate stages content in a private candidate beside path, runs
// the configured validator on it and always removes the candidate again. The
// steps run in a fixed order: parent safety check, CreateTemp, write/sync/
// close, validator exec, cleanup. A cleanup failure is reported on its own
// when everything else succeeded, or appended to the earlier error otherwise.
func (f *File) validateCandidate(path string, content []byte) (err error) {
	parent := filepath.Dir(path)
	if err := verifyValidationParent(parent); err != nil {
		return fmt.Errorf("file %s: validation candidate parent: %w", path, err)
	}
	candidate, err := createValidationCandidate(parent, candidateNamePattern(path))
	if err != nil {
		return fmt.Errorf("file %s: create validation candidate: %w", path, err)
	}
	candidatePath := candidate.Name()
	// closed is only set once writeValidationCandidate has closed the file
	// successfully. Until then the deferred cleanup closes it, which also
	// covers early error returns and panics between CreateTemp and Close.
	closed := false
	defer func() { err = cleanupValidationCandidate(path, candidate, closed, err) }()

	if err := writeValidationCandidate(path, candidate, content); err != nil {
		return err
	}
	closed = true
	return f.runValidation(path, candidatePath)
}

// validationCandidateFile is the part of *os.File that staging a candidate
// needs. It lets tests inject write/sync/close failures and panics.
type validationCandidateFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// createValidationCandidate creates the private (0600) candidate file. It is
// a variable only so tests can wrap the real file with failure injection to
// check validateCandidate's close/remove wiring; production code never
// reassigns it.
var createValidationCandidate = func(dir, pattern string) (validationCandidateFile, error) {
	candidate, err := os.CreateTemp(dir, pattern)
	if err != nil {
		// Return an untyped nil, never a nil *os.File wrapped in the interface.
		return nil, err
	}
	return candidate, nil
}

// writeValidationCandidate writes, fsyncs and closes the candidate so the
// validator sees the complete bytes. It returns at the first failure without
// closing; the caller's deferred cleanupValidationCandidate closes the file
// in that case.
func writeValidationCandidate(path string, candidate validationCandidateFile, content []byte) error {
	if _, err := candidate.Write(content); err != nil {
		return fmt.Errorf("file %s: write validation candidate: %w", path, err)
	}
	if err := candidate.Sync(); err != nil {
		return fmt.Errorf("file %s: sync validation candidate: %w", path, err)
	}
	if err := candidate.Close(); err != nil {
		return fmt.Errorf("file %s: close validation candidate: %w", path, err)
	}
	return nil
}

// cleanupValidationCandidate closes the candidate unless it was already
// closed successfully, then unlinks it. A close error only becomes the result
// when nothing failed earlier; otherwise the earlier error is kept (a failed
// Close in writeValidationCandidate leads to a second Close here whose
// "already closed" error is dropped for that reason).
func cleanupValidationCandidate(path string, candidate validationCandidateFile, closed bool, err error) error {
	if !closed {
		if closeErr := candidate.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("file %s: close validation candidate: %w", path, closeErr)
		}
	}
	return removeValidationCandidate(path, candidate.Name(), err)
}

// removeValidationCandidate unlinks the candidate and folds a removal failure
// into err. An already-missing candidate is fine (e.g. the validator deleted
// it). When err is already set it stays the wrapped, primary error and the
// cleanup failure is appended as text only.
func removeValidationCandidate(path, candidatePath string, err error) error {
	removeErr := os.Remove(candidatePath)
	if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
		return err
	}
	cleanupErr := fmt.Errorf("file %s: remove validation candidate: %w", path, removeErr)
	if err == nil {
		return cleanupErr
	}
	return fmt.Errorf("%w; %v", err, cleanupErr)
}

// runValidation executes the validator directly (no shell) with the
// CandidatePath placeholder replaced by the staged candidate's path. See
// runValidatorCommand for how the process is bounded and what the error
// carries; the "file <path>: validation by <bin> failed: " prefix is stable.
func (f *File) runValidation(path, candidatePath string) error {
	args := substituteCandidatePath(f.validationArgs, candidatePath)
	if err := runValidatorCommand(f.validationBin, args); err != nil {
		return fmt.Errorf("file %s: validation by %s failed: %w", path, f.validationBin, err)
	}
	return nil
}

const (
	// validatorOutputLimit bounds the validator output that reaches an
	// error: at most this many raw bytes are kept, and the sanitized text
	// rendered from them is cut to this many bytes again (sanitizing can
	// grow it, e.g. "\n" becomes " | "). Output beyond the raw limit is
	// still read (so the validator never blocks on a full pipe) but only
	// counted.
	validatorOutputLimit = 4096
	// validatorWaitDelay bounds how long Wait keeps waiting for the output
	// pipe after the validator exited or was killed. Only the validator
	// itself is killed on timeout; a descendant that inherited its stdout or
	// stderr could otherwise hold the pipe, and so the apply, open forever.
	validatorWaitDelay = 2 * time.Second
)

// runValidatorCommand runs one validator (argv only, never a shell) and
// returns nil when it exits 0. It is the shared executor for every file
// validator and deliberately has no path prefix in its errors, so other
// validating resources in this package can wrap it with their own.
//
// It is bounded like every backend command by internal/exec's process-wide
// default timeout (api.SetCommandTimeout, CLI -cmd-timeout, 5m by default),
// but does not use exec.RunWith because that keeps unbounded output. The
// validator:
//   - gets stdin from /dev/null, so it never reads gonf's plan stream;
//   - stays in gonf's process group, so terminal signals (Ctrl-C, hangup,
//     Ctrl-Z) reach it exactly as they reach gonf;
//   - is killed (SIGKILL, the validator process only) when the timeout
//     expires. A kill that fails (EPERM, e.g. a validator run through
//     sudo/doas by a non-root gonf) cannot bound it: Wait then lasts until
//     it exits, though exec closes the output pipe validatorWaitDelay after
//     the failed kill, so its next write may end it with SIGPIPE ("signal:
//     broken pipe"). Descendants it started are not killed; one still holding
//     the output pipe is cut off after validatorWaitDelay, after which
//     runValidatorCommand returns without waiting for it. The verdict is
//     the validator's own exit status: a deadline expiring while only such
//     a descendant is left does not turn it into a timeout;
//   - has its stdout and stderr captured together into one bounded buffer.
//
// A failure is the exit error, the timeout error or the start error, followed
// by ": validator output: <sanitized output>" when it printed anything. Only
// what the validator prints is included; gonf never adds the candidate's
// content, but a validator that echoes its input will leak it.
func runValidatorCommand(bin string, args []string) error {
	timeout := gexec.DefaultTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output := &cappedOutput{limit: validatorOutputLimit}
	// Stdin stays nil (/dev/null). Stdout and stderr share one writer, which
	// exec turns into one pipe, so their relative order is preserved.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = validatorWaitDelay
	// exec calls Cancel at most once, from its context watcher, and only if
	// the deadline wins the race against Wait reaping the validator; a
	// deadline expiring later, while Wait merely drains a pipe held by a
	// lingering descendant, never reaches it. When reap and deadline
	// coincide, Cancel may still run just after the reap: Kill then returns
	// os.ErrProcessDone and nothing is recorded. It may also run just
	// before the reap, on a validator that already exited (a zombie): Kill
	// then succeeds without effect and the flag is set anyway. The flag
	// therefore only says "we signalled it"; validatorTimedOut decides.
	var killedByTimeout atomic.Bool
	cmd.Cancel = func() error { return killForTimeout(cmd.Process.Kill, &killedByTimeout) }
	err := cmd.Run()
	return withValidatorOutput(validatorRunError(cmd.ProcessState, killedByTimeout.Load(), timeout, err), output)
}

// killForTimeout is the validator's exec.Cmd.Cancel: it kills the process
// and records that in killed, but only when the kill was delivered (a failed
// kill, e.g. EPERM or os.ErrProcessDone, leaves the process to its own fate
// and must not turn its verdict into a timeout).
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
// in runValidatorCommand: a validator that exited, with any status, before
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
//     validatorWaitDelay) or a deadline that expired after the exit;
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

// substituteCandidatePath returns a copy of args in which every element equal
// to opt.CandidatePath is replaced by candidatePath. Only whole-element
// matches are replaced; the caller's slice is never modified.
func substituteCandidatePath(args []string, candidatePath string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		if arg == opt.CandidatePath {
			out[i] = candidatePath
			continue
		}
		out[i] = arg
	}
	return out
}

// validationFstat and validationGeteuid are the only identity and attribute
// inputs of the candidate parent checks: the walk itself always opens the
// real directories, but what each opened directory is judged by comes from
// validationFstat. They are variables so tests can feed synthetic owners and
// modes (foreign uids, a writable root, ...) that do not depend on the host
// or on the ancestry of $TMPDIR. Production code never reassigns them.
var (
	validationFstat   = func(fd int, _ string) (safepath.Info, error) { return safepath.Fstat(fd) }
	validationGeteuid = os.Geteuid
)

// verifyValidationParent makes the candidate pathname safe to hand to an
// external validator after the file has been closed. CreateTemp's 0600 mode
// protects its bytes, but an untrusted writer of the parent could unlink and
// replace the candidate between Close and exec. A safe single-file contract
// therefore needs a real parent owned by the applying uid and not writable by
// its group or other users. Normal system configuration parents (/etc,
// /var/nsd/etc, ...) satisfy this; callers needing a shared writable staging
// area need a separate multi-file/staging design instead.
//
// The path is walked from "/" with the shared descriptor walk of
// internal/safepath (every component opened O_NOFOLLOW relative to its
// parent's descriptor, nothing created), and each component is judged on the
// descriptor that was opened, so what is checked is exactly the directory the
// walk continues in, not a name that may have been swapped since an lstat.
// Components are checked top-down, so the first unsafe ancestor is the one
// reported and nothing below it is opened. The root itself is only checked
// when it is the parent (see verifyValidationComponent). The final descriptor
// is closed again: CreateTemp and the validator work by path, which is sound
// because the rules above leave nobody but root and the applying uid able to
// rename or replace anything along it.
func verifyValidationParent(parent string) error {
	currentUID := uint32(validationGeteuid())
	if !filepath.IsAbs(parent) {
		return fmt.Errorf("%s is not an absolute path", parent)
	}
	components, err := validationPathComponents(parent)
	if err != nil {
		return err
	}
	root := filepath.VolumeName(parent) + string(filepath.Separator)
	walk := safepath.Walk{Check: func(c safepath.Component) error {
		return verifyValidationComponent(c, currentUID)
	}}
	fd, err := walk.Open(root, components)
	if err != nil {
		return validationWalkError(err)
	}
	return unix.Close(fd)
}

// validationWalkError words a component the walk could not open. A refusal
// of verifyValidationComponent is already worded and returned as it is.
func validationWalkError(err error) error {
	var ce *safepath.ComponentError
	if !errors.As(err, &ce) {
		return err
	}
	switch {
	case errors.Is(ce.Err, safepath.ErrSymlink):
		return fmt.Errorf("%s contains a symlink path component", ce.Path)
	case errors.Is(ce.Err, unix.ENOTDIR):
		return fmt.Errorf("%s is not a directory", ce.Path)
	}
	return fmt.Errorf("inspect %s: %w", ce.Path, ce)
}

// verifyValidationComponent checks one directory on the path to the candidate
// parent, as the walk opened it: it is a real directory already (the walk
// opens components O_DIRECTORY|O_NOFOLLOW and refuses symlinks and other
// files). The last one directly holds the candidate and must be private to the
// applying uid; intermediates only need to stop others from renaming what lies
// below them. When the parent is the filesystem root itself, the root is the
// last (and only) component and gets the private rule.
func verifyValidationComponent(c safepath.Component, currentUID uint32) error {
	info, err := validationFstat(c.FD, c.Path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", c.Path, err)
	}
	if c.Last {
		return verifyValidationPrivateDir(c.Path, info, currentUID)
	}
	return verifyValidationIntermediate(c.Path, info, currentUID)
}

// verifyValidationPrivateDir requires the directory holding the candidate to
// be owned by the applying uid and not writable by group or other users.
// Unlike for intermediates, the sticky bit does not relax this rule.
func verifyValidationPrivateDir(path string, info safepath.Info, currentUID uint32) error {
	if info.UID != currentUID {
		return fmt.Errorf("%s is not owned by the applying uid", path)
	}
	if info.Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or other users", path)
	}
	return nil
}

// verifyValidationIntermediate accepts an ancestor owned by root or the
// applying uid. It may be group/other-writable only with the sticky bit (as
// /tmp is), which stops other users from renaming or replacing our subtree.
func verifyValidationIntermediate(path string, info safepath.Info, currentUID uint32) error {
	if info.UID != 0 && info.UID != currentUID {
		return fmt.Errorf("%s is owned by an untrusted uid", path)
	}
	if info.Perm()&0o022 != 0 && info.Perm()&0o1000 == 0 {
		return fmt.Errorf("%s is writable by group or other users without sticky protection", path)
	}
	return nil
}

// validationPathComponents returns the raw directory components without
// resolving them through filepath.Clean. Rejecting ".." (which the safepath
// walk would otherwise follow to the parent) and refusing a symlink at every
// component prevents a symlink/parent alias from making the path checked here
// differ from the path used by CreateTemp and the validator.
func validationPathComponents(path string) ([]string, error) {
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, volume)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	var components []string
	for _, component := range strings.Split(rest, string(filepath.Separator)) {
		switch component {
		case "", ".":
			continue
		case "..":
			return nil, fmt.Errorf("%s contains a parent-directory path component", path)
		default:
			components = append(components, component)
		}
	}
	return components, nil
}

func candidateNamePattern(path string) string {
	const (
		marker  = ".gonfvalidate"
		maxRand = 10
		nameMax = 255
	)
	base := filepath.Base(path)
	if max := nameMax - len(marker) - maxRand; len(base) > max {
		base = base[:max]
	}
	return base + marker + "*"
}
