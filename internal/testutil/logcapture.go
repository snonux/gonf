package testutil

import (
	"strings"
	"sync"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/testseam"
)

// logBuffer is the concurrency-safe sink CaptureLog redirects the logger to,
// so apply code logging from another goroutine cannot race the test reading
// the output.
type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

// CaptureLog redirects internal/logger output into memory at level l,
// without timestamps so lines can be compared exactly
// (logger.RedirectUnprefixed), until t's cleanup restores the previous
// destination and level. output returns what was logged so far. The capture
// is process-global, so a test using it must not run in parallel: like
// every intentionally global test hook it calls t.Setenv
// (testseam.ParallelGuardEnv), and testing then panics in a parallel test.
func CaptureLog(t testing.TB, l logger.Level) (output func() string) {
	t.Helper()
	t.Setenv(testseam.ParallelGuardEnv, "1")
	b := &logBuffer{}
	t.Cleanup(logger.RedirectUnprefixed(b, l))
	return b.String
}

// Write appends p to the buffer.
func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
