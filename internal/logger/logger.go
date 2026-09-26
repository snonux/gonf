// Package logger provides leveled logging for gonf.
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// Level controls which messages are emitted. A higher level includes all
// lower (more severe) levels.
type Level int

const (
	// LevelError emits only errors.
	LevelError Level = iota
	// LevelWarn emits warnings and errors.
	LevelWarn
	// LevelInfo emits informational messages and above (default).
	LevelInfo
	// LevelDebug emits everything, including debug output.
	LevelDebug
)

var (
	mu    sync.Mutex
	level = LevelInfo
	std   = log.New(os.Stderr, "", log.LstdFlags)
	// redactor, when set, rewrites every formatted message before it is
	// written (SetRedactor).
	redactor Redactor
)

// SetRedactor installs r to rewrite every log message before it is written,
// and to redact Redact and RedactingWriter output, or removes it with nil.
// api installs the secret registry (secret.Values), so a resolved secret
// never reaches a controller-side log line — including a registration debug
// line or a declaration error message that quotes a resource identity. r must be safe
// for concurrent use and must not log.
func SetRedactor(r Redactor) {
	mu.Lock()
	defer mu.Unlock()
	redactor = r
}

// SetLevel sets the minimum verbosity. Default is LevelInfo.
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// GetLevel returns the current log level.
func GetLevel() Level {
	mu.Lock()
	defer mu.Unlock()
	return level
}

// LevelFlag returns the global gonf flag that selects l in a child gonf
// process: "-verbose" for LevelDebug, "-quiet" for LevelWarn and LevelError
// (the closest a flag gets), and "" for the default LevelInfo. The local
// elevated re-exec and the remote apply pass it before "apply", so the child
// logs at the controller's level instead of its own default.
func LevelFlag(l Level) string {
	switch {
	case l >= LevelDebug:
		return "-verbose"
	case l <= LevelWarn:
		return "-quiet"
	default:
		return ""
	}
}

// RedirectUnprefixed sends log output to w at level l, with no timestamp or
// other prefix, until restore reinstates the previous destination and level.
// It is test-oriented: the missing prefix lets captured lines be compared
// exactly, and its one caller is internal/testutil.CaptureLog. The redirect
// is process-global, so callers must not run in parallel with other code
// that logs or redirects. w must be safe for concurrent use if anything logs
// from several goroutines.
func RedirectUnprefixed(w io.Writer, l Level) (restore func()) {
	mu.Lock()
	prevStd, prevLevel := std, level
	std, level = log.New(w, "", 0), l
	mu.Unlock()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		std, level = prevStd, prevLevel
	}
}

// logf reads the level and the destination logger under mu, so
// RedirectUnprefixed (a test's log capture) can swap std without a data race;
// the write itself happens outside the lock (log.Logger serialises its own
// output).
func logf(msgLevel Level, format string, args ...any) {
	mu.Lock()
	cur, out, rewrite := level, std, redactor
	mu.Unlock()
	if msgLevel > cur {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if rewrite != nil {
		msg = rewrite.Redact(msg)
	}
	_ = out.Output(3, msg)
}

// currentRedactor returns the redactor SetRedactor installed, or nil.
func currentRedactor() Redactor {
	mu.Lock()
	defer mu.Unlock()
	return redactor
}

// Error logs a formatted message at LevelError.
func Error(format string, args ...any) { logf(LevelError, format, args...) }

// Warn logs a formatted message at LevelWarn.
func Warn(format string, args ...any) { logf(LevelWarn, format, args...) }

// Info logs a formatted message at LevelInfo.
func Info(format string, args ...any) { logf(LevelInfo, format, args...) }

// Debug logs a formatted message at LevelDebug.
func Debug(format string, args ...any) { logf(LevelDebug, format, args...) }
