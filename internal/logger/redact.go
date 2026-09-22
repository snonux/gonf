package logger

import (
	"bytes"
	"io"
	"sync"
)

// maxPendingLine bounds how much of an unterminated line a RedactingWriter
// holds back; a longer run is redacted and forwarded as it is.
const maxPendingLine = 64 << 10

// RedactingWriter forwards what is written to it to its destination one
// complete line at a time, each line passed through Redact first, so a
// secret split across two writes is still redacted. It is for output gonf
// relays but did not format itself: an elevated apply child or a remote
// gonf over ssh, neither of which has the controller's secret registry.
// Close forwards a final unterminated line. It is safe for concurrent use.
type RedactingWriter struct {
	mu      sync.Mutex
	w       io.Writer
	pending []byte
}

// NewRedactingWriter returns a RedactingWriter forwarding to w.
func NewRedactingWriter(w io.Writer) *RedactingWriter {
	return &RedactingWriter{w: w}
}

// Redact applies the redactor installed with SetRedactor to s, or returns s
// unchanged when there is none. Output gonf writes with fmt rather than the
// logger (the apply summary, CLI errors) goes through it.
func Redact(s string) string {
	mu.Lock()
	rewrite := redact
	mu.Unlock()
	if rewrite == nil {
		return s
	}
	return rewrite(s)
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
		if err := r.forward(r.pending); err != nil {
			return 0, err
		}
		r.pending = nil
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

// forward writes one redacted chunk to the destination.
func (r *RedactingWriter) forward(line []byte) error {
	_, err := io.WriteString(r.w, Redact(string(line)))
	return err
}
