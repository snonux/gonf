package logger

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// maxPendingLine bounds how much of an unterminated line a RedactingWriter
// holds back; beyond it the writer forwards what the redactor says is safe
// (Redactor.FlushPoint) and keeps only the rest.
const maxPendingLine = 64 << 10

// RelayWaitDelay bounds how long RunRelayed waits, after the relayed process
// exited or was killed, for its output pipe to reach end of file. A
// descendant that inherited the pipe (an orphaned root gonf apply under sudo
// without use_pty, after the context killed sudo) holds it open; RunRelayed
// then returns anyway and keeps draining the pipe in the background, so the
// descendant's later output is still relayed (redacted) and it never dies of
// SIGPIPE. It matches internal/validator's WaitDelay.
const RelayWaitDelay = 2 * time.Second

// Redactor rewrites text that may quote a secret. api installs the secret
// registry (secret.Values) with SetRedactor.
type Redactor interface {
	// Redact returns s with every secret it knows replaced.
	Redact(s string) string
	// FlushPoint returns how many leading bytes of s can be redacted and
	// written now without splitting a secret that may continue in bytes
	// not seen yet: no match of the redactor may cross it.
	FlushPoint(s string) int
}

// RedactingWriter forwards what is written to it to its destination one
// complete line at a time, each line passed through Redact first, so a
// secret split across two writes is still redacted (a multi-line secret is
// caught line by line: secret.Values tracks its strong lines as forms of
// their own). An unterminated run longer than maxPendingLine is forwarded
// only up to the redactor's FlushPoint. It is for output gonf relays but did
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
// relay then waits at most RelayWaitDelay for the pipe's end of file. When a
// descendant still holds the pipe, RunRelayed returns and a background
// goroutine keeps relaying until the descendant closes it — its later output
// may then appear after RunRelayed's caller moved on, but it is never lost
// and the descendant never gets SIGPIPE.
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
	drained := make(chan struct{})
	go relayPipe(pr, w, drained)
	err = cmd.Wait()
	select {
	case <-drained:
	case <-time.After(RelayWaitDelay):
		Debug("%s exited but a process it started still holds its output; relaying it in the background", cmd.Path)
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
func (r *RedactingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = append(r.pending, p...)
	for {
		i := bytes.IndexByte(r.pending, '\n')
		if i < 0 {
			break
		}
		if err := r.forward(r.pending[:i+1]); err != nil {
			return 0, err
		}
		r.pending = r.pending[i+1:]
	}
	if len(r.pending) > maxPendingLine {
		if err := r.forwardSafePrefix(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Close forwards the final unterminated line, if any.
func (r *RedactingWriter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 {
		return nil
	}
	err := r.forward(r.pending)
	r.pending = nil
	return err
}

// forwardSafePrefix forwards the part of an overlong pending run that no
// secret can still extend into (Redactor.FlushPoint), keeping the rest.
func (r *RedactingWriter) forwardSafePrefix() error {
	cut := len(r.pending)
	if red := currentRedactor(); red != nil {
		cut = red.FlushPoint(string(r.pending))
	}
	if cut <= 0 {
		return nil
	}
	if err := r.forward(r.pending[:cut]); err != nil {
		return err
	}
	r.pending = append([]byte(nil), r.pending[cut:]...)
	return nil
}

// relayPipe copies pr to w through a RedactingWriter until end of file,
// then closes pr and done.
func relayPipe(pr *os.File, w io.Writer, done chan<- struct{}) {
	defer close(done)
	out := NewRedactingWriter(w)
	_, _ = io.Copy(out, pr)
	_ = out.Close()
	_ = pr.Close()
}

// Write forwards Redact(p); it reports len(p) unless w fails.
func (r redactedWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, Redact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// forward writes one redacted chunk to the destination.
func (r *RedactingWriter) forward(line []byte) error {
	_, err := io.WriteString(r.w, Redact(string(line)))
	return err
}
