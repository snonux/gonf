package logger

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// maxPendingLine bounds how much of an unterminated line a RedactingWriter
// holds back when no redactor is installed (Write uses
// Redactor.MaxPending instead once SetRedactor installs one, so the real
// threshold tracks the installed redactor's own split-guard rather than an
// independent literal here — see Redactor.MaxPending and
// secret.Values.MaxPending). internal/logger cannot import secret to share
// its MaxSplitGuard constant directly: secret imports internal/safepath,
// whose own tests import internal/testutil, which imports this package, so
// internal/logger importing secret would close that cycle (go vet catches
// it as "import cycle not allowed in test"). Routing the threshold through
// the Redactor interface instead avoids the cycle and, per its own doc,
// keeps a future second Redactor from duplicating the same literal.
const maxPendingLine = 64 << 10

// RelayWaitDelay bounds how long RunRelayed waits, after the relayed process
// exited or was killed, for its output pipe to reach end of file. A
// descendant that inherited the pipe (an orphaned root gonf apply under sudo
// without use_pty, after the context killed sudo) holds it open; RunRelayed
// then hands the pipe over (see RunRelayed) and returns. It matches
// internal/validator's WaitDelay.
const RelayWaitDelay = 2 * time.Second

// relayWaitDelay is the delay RunRelayed uses: RelayWaitDelay, shortened or
// lengthened by tests only.
var relayWaitDelay = RelayWaitDelay

// Redactor rewrites text that may quote a secret and controls how
// RedactingWriter forces a flush of an overlong unterminated line. api
// installs the secret registry (secret.Values) with SetRedactor.
type Redactor interface {
	// Redact returns s with every secret it knows replaced.
	Redact(s string) string
	// FlushPoint returns the already-redacted text a relay may forward now
	// (out) and how many of s's leading bytes that consumed (consumed; 0
	// means nothing yet): no match of the redactor may start before
	// consumed and cross it. FlushPoint computes the redaction itself
	// instead of handing the caller a plain cut to redact independently,
	// because a safe cut is not always one FlushPoint's own Redact can
	// re-span correctly on the caller's side: forced past a self-overlapping
	// match chain that never resolves (secret.Values.FlushPoint's escape
	// hatch), it may need to treat the whole consumed prefix as one opaque
	// redaction rather than let per-span matching run on just that prefix,
	// which — missing the fuller context FlushPoint had — could leave a
	// dangling unmatched fragment at the cut boundary.
	FlushPoint(s string) (out string, consumed int)
	// MaxPending is the longest an unterminated line RedactingWriter may
	// buffer before forcing a flush through FlushPoint. It must be at least
	// as large as the redactor's own split-guard threshold
	// (secret.MaxSplitGuard for the production redactor): FlushPoint's
	// escape hatch for a self-overlapping match chain only has a safe,
	// progress-making answer once s already exceeds that threshold, so a
	// smaller MaxPending would force a flush before FlushPoint can make one,
	// returning no progress and reintroducing the unbounded buffer growth
	// task mb2 fixed.
	MaxPending() int
}

// RedactingWriter forwards what is written to it to its destination one
// complete line at a time, each line passed through Redact first, so a
// secret split across two writes is still redacted (a multi-line secret is
// caught line by line: secret.Values tracks its strong lines as forms of
// their own). An unterminated run longer than the installed redactor's
// MaxPending (maxPendingLine with none installed) is forwarded only up to
// the redactor's FlushPoint. It is for output gonf relays but did
// not format itself: an elevated apply child or a remote gonf over ssh,
// neither of which has the controller's secret registry. Close forwards the
// rest. It is safe for concurrent use.
type RedactingWriter struct {
	mu      sync.Mutex
	w       io.Writer
	pending []byte
}

// redactedWriter is NewRedactedWriter's writer.
type redactedWriter struct{ w io.Writer }

// NewRedactingWriter returns a RedactingWriter forwarding to w.
func NewRedactingWriter(w io.Writer) *RedactingWriter {
	return &RedactingWriter{w: w}
}

// Redact applies the redactor installed with SetRedactor to s, or returns s
// unchanged when there is none. Output gonf writes with fmt rather than the
// logger (the apply summary, CLI errors, push summary lines) goes through
// it.
func Redact(s string) string {
	r := currentRedactor()
	if r == nil {
		return s
	}
	return r.Redact(s)
}

// RunRelayed runs cmd with its stdout and stderr relayed to w through a
// RedactingWriter and returns cmd's own result. The output goes through a
// pipe gonf creates itself (not os/exec's copying goroutine, whose
// WaitDelay would close the pipe and turn a clean exit into
// exec.ErrWaitDelay): Wait therefore returns as soon as cmd exits, and the
// relay then waits at most RelayWaitDelay for the pipe's end of file.
//
// When a descendant still holds the pipe after that (an orphan), gonf must
// neither wait for it nor, by closing the pipe's read end — as its own exit
// eventually would — kill it with SIGPIPE mid-apply. So when w is a file
// (os.Stderr in production) RunRelayed stops relaying and hands the read end
// to a detached `cat` writing straight to w (handOff): the orphan keeps a
// reader for as long as it writes, even after gonf has exited, exactly as it
// kept the terminal before gonf relayed its output. What it writes after the
// hand-off is NOT redacted. When w is not a file, or cat cannot be started,
// a background goroutine keeps relaying (redacted) instead, which protects
// the orphan only while gonf runs.
func RunRelayed(cmd *exec.Cmd, w io.Writer) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	_ = pw.Close() // the child holds its own copy
	stopped := make(chan bool, 1)
	go relayPipe(pr, w, stopped)
	err = cmd.Wait()
	select {
	case <-stopped:
	case <-time.After(relayWaitDelay):
		releaseOrphan(cmd.Path, pr, w, stopped)
	}
	return err
}

// NewRedactedWriter returns a writer that passes every Write through Redact
// before forwarding it to w, for callers that write whole messages in one
// call (the push/preview summary lines); unlike RedactingWriter it buffers
// nothing.
func NewRedactedWriter(w io.Writer) io.Writer {
	return redactedWriter{w: w}
}

// Write buffers p and forwards every completed line, redacted. It reports
// len(p) unless the destination fails.
//
// currentRedactor is read exactly once, up front, and that single value is
// threaded through every decision this call makes (each forward, the
// pendingLimit threshold, and forwardSafePrefix). Production never clears
// the installed redactor, but tests do (SetRedactor(nil) via t.Cleanup); if
// Write instead re-read the package-global at each step, a SetRedactor
// landing mid-call could make it pick pendingLimit's threshold from one
// redactor and then, past that threshold, take forwardSafePrefix's nil
// branch — which forwards the entire pending buffer unredacted. Reading
// once makes that impossible: this call sees one redactor state throughout.
func (r *RedactingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	red := currentRedactor()
	r.pending = append(r.pending, p...)
	for {
		i := bytes.IndexByte(r.pending, '\n')
		if i < 0 {
			break
		}
		if err := r.forward(red, r.pending[:i+1]); err != nil {
			return 0, err
		}
		r.pending = r.pending[i+1:]
	}
	if len(r.pending) > pendingLimit(red) {
		if err := r.forwardSafePrefix(red); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// pendingLimit returns red's Redactor.MaxPending, so the forced-flush
// threshold tracks its split-guard, or maxPendingLine's default when red is
// nil (nothing to protect a cut from splitting then). red is the caller's
// single currentRedactor() read (see Write's doc), not re-read here, so this
// always agrees with whatever redactor state the rest of the same call used.
func pendingLimit(red Redactor) int {
	if red != nil {
		return red.MaxPending()
	}
	return maxPendingLine
}

// Close forwards the final unterminated line, if any.
func (r *RedactingWriter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 {
		return nil
	}
	err := r.forward(currentRedactor(), r.pending)
	r.pending = nil
	return err
}

// Write forwards Redact(p); it reports len(p) unless w fails.
func (r redactedWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, Redact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// forwardSafePrefix forwards the already-redacted part of an overlong
// pending run that no secret can still extend into (Redactor.FlushPoint
// both finds and redacts it — see the interface doc for why the caller must
// not redact that prefix itself), keeping the rest pending. red is nil when
// no redactor is installed, in which case there is nothing to protect, so it
// forwards (and clears) everything pending; red is the caller's single
// currentRedactor() read (see Write's doc), never re-read here.
//
// consumed is clamped to len(r.pending) before it slices: FlushPoint is part
// of the exported Redactor interface (SetRedactor takes any implementation),
// so a third-party redactor that returns consumed > len(s) — buggy or
// malicious — must not panic this relay goroutine mid-apply; it is instead
// treated as "consume everything currently pending". out is written exactly
// as FlushPoint returned it, never re-derived: forwardSafePrefix trusts out
// to already be the correctly redacted text for the prefix FlushPoint claims
// to have consumed (it has no way to re-verify a black-box redactor's own
// redaction without redoing it, which would defeat the point of the
// interface). A redactor whose out under-covers an overclaimed consumed —
// the same misbehaviour the clamp above already tolerates, taken further —
// therefore causes the uncovered surplus to be silently dropped once
// consumed is clamped, rather than forwarded raw (a leak) or left pending
// forever (unbounded growth): of the three, silent loss is the only one that
// keeps both the confidentiality and the boundedness promise.
func (r *RedactingWriter) forwardSafePrefix(red Redactor) error {
	if red == nil {
		if err := r.forward(nil, r.pending); err != nil {
			return err
		}
		r.pending = nil
		return nil
	}
	out, consumed := red.FlushPoint(string(r.pending))
	if consumed <= 0 {
		return nil
	}
	consumed = min(consumed, len(r.pending))
	if _, err := io.WriteString(r.w, out); err != nil {
		return err
	}
	r.pending = append([]byte(nil), r.pending[consumed:]...)
	return nil
}

// relayPipe copies pr to w through a RedactingWriter until end of file (it
// then closes pr) or until pr's read deadline stops it (pr stays open for
// handOff), and reports on stopped whether it reached end of file.
func relayPipe(pr *os.File, w io.Writer, stopped chan<- bool) {
	out := NewRedactingWriter(w)
	_, err := io.Copy(out, pr)
	_ = out.Close()
	if errors.Is(err, os.ErrDeadlineExceeded) {
		stopped <- false
		return
	}
	_ = pr.Close()
	stopped <- true
}

// releaseOrphan is RunRelayed's path for a pipe still held after the relay
// delay: stop the relay (a read deadline) and hand pr to a detached cat
// writing to w; when that is impossible, let the relay keep draining in the
// background.
func releaseOrphan(name string, pr *os.File, w io.Writer, stopped chan bool) {
	f, isFile := w.(*os.File)
	if !isFile || pr.SetReadDeadline(time.Now()) != nil {
		Debug("%s exited but a process it started still holds its output; relaying it in the background while gonf runs", name)
		return
	}
	if eof := <-stopped; eof {
		return // the orphan closed the pipe meanwhile
	}
	if err := handOff(pr, f); err != nil {
		Warn("%s exited but a process it started still holds its output, and handing it over failed (%v); relaying it while gonf runs", name, err)
		_ = pr.SetReadDeadline(time.Time{})
		go relayPipe(pr, w, stopped)
		return
	}
	Debug("%s exited but a process it started still holds its output; handed it to the terminal unredacted", name)
}

// handOff starts `cat` with pr as its input and f as its output, in a new
// session without a controlling terminal, so a terminal Ctrl-C or hangup
// meant for gonf does not reach it and `stty tostop` cannot stop it with
// SIGTTOU (a background process group on gonf's terminal would be stopped,
// then killed by the hangup once gonf exits, leaving the orphan to die of
// SIGPIPE). It then closes gonf's copy of pr and reaps cat in the
// background. cat exits when the last writer of the pipe closes it.
func handOff(pr, f *os.File) error {
	helper := exec.Command("cat")
	helper.Stdin, helper.Stdout, helper.Stderr = pr, f, f
	helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := helper.Start(); err != nil {
		return err
	}
	_ = pr.Close()
	go func() { _ = helper.Wait() }()
	return nil
}

// forward writes one chunk to the destination, redacted with red (nil
// forwards it unchanged). red is the caller's single currentRedactor() read
// (see Write's doc) rather than a fresh lookup here, so every chunk a single
// Write or Close call forwards is redacted against the same redactor state.
func (r *RedactingWriter) forward(red Redactor, line []byte) error {
	s := string(line)
	if red != nil {
		s = red.Redact(s)
	}
	_, err := io.WriteString(r.w, s)
	return err
}
