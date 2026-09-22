package api

import (
	"io"
	"sync"
)

// lockedWriter serialises Writes to w, so several goroutines can share one
// destination that is not itself safe for concurrent use. A fleet run hands
// one summary writer to every concurrent cluster group (see
// groupRun.deliverGroups); the production destination, os.Stderr, tolerates
// that, but an injected one (a test's bytes.Buffer via pushOutput) does
// not. Each summary line is written with a single Fprintf, i.e. one Write,
// so serialising Writes also keeps every line whole.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// newLockedWriter returns a lockedWriter forwarding to w.
func newLockedWriter(w io.Writer) *lockedWriter {
	return &lockedWriter{w: w}
}

// Write forwards p to the destination while holding the lock and returns
// the destination's result unchanged.
func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
