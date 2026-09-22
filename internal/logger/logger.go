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
// line or a Fatal message that quotes a resource identity. r must be safe
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

// Redirect sends log output to w at level l, without the timestamp prefix so
// lines can be compared exactly, until restore reinstates the previous
// destination and level. The redirect is process-global: callers (tests
// capturing log lines, through internal/testutil.CaptureLog) must not run in
// parallel with other code that logs or redirects. w must be safe for
// concurrent use if anything logs from several goroutines.
func Redirect(w io.Writer, l Level) (restore func()) {
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

// logf reads the level and the destination logger under mu, so Redirect
// (a test's log capture) can swap std without a data race; the write itself
// happens outside the lock (log.Logger serialises its own output).
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

// Fatal logs at Error level, runs the cleanups registered with OnFatal and
// exits the process. It is reserved for registration-time DSL misuse
// (duplicate Task/Host/Fleet registration, unsupported or conflicting
// options, invalid patterns) and Must* lookups: programmer errors that abort
// the recipe before anything is applied. The fail-fast contract is documented
// in docs/plan.md, "Error handling contract". Apply-time code must return
// errors instead, so deferred cleanup (temp plan and apply run dirs) always
// runs and gonf stays embeddable as a library. os.Exit skips deferred
// functions, so code that holds copies of secrets on disk while a task body
// runs registers its removal with OnFatal.
func Fatal(format string, args ...any) {
	logf(LevelError, format, args...)
	runFatalHooks()
	os.Exit(1)
}

// fatalHook is one OnFatal registration; id makes unregistration exact.
type fatalHook struct {
	id int
	fn func()
}

var (
	hookMu     sync.Mutex
	hooks      []fatalHook
	nextHookID int
)

// OnFatal registers fn to run when Fatal is called, just before the process
// exits, and returns the function that unregisters it again. It exists for
// cleanup that a deferred call cannot provide because os.Exit skips defers:
// a hook makes the fail-fast path remove temporary directories that may hold
// copies of secrets. Hooks run once each, most recently registered first,
// and must be quick and must not call Fatal. It does not cover SIGKILL or a
// crash; SIGINT/SIGTERM are handled by the caller's signal context.
func OnFatal(fn func()) (unregister func()) {
	hookMu.Lock()
	defer hookMu.Unlock()
	nextHookID++
	id := nextHookID
	hooks = append(hooks, fatalHook{id: id, fn: fn})
	return func() {
		hookMu.Lock()
		defer hookMu.Unlock()
		for i, h := range hooks {
			if h.id == id {
				hooks = append(hooks[:i], hooks[i+1:]...)
				return
			}
		}
	}
}

// runFatalHooks takes all registered hooks (so each runs at most once and a
// hook that panics or re-enters cannot loop) and runs them outside the lock,
// newest first, like deferred calls.
func runFatalHooks() {
	hookMu.Lock()
	pending := hooks
	hooks = nil
	hookMu.Unlock()
	for i := len(pending) - 1; i >= 0; i-- {
		pending[i].fn()
	}
}
