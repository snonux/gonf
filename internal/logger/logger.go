// Package logger provides leveled logging for gonf.
package logger

import (
	"fmt"
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
)

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

func logf(msgLevel Level, format string, args ...any) {
	mu.Lock()
	cur := level
	mu.Unlock()
	if msgLevel > cur {
		return
	}
	_ = std.Output(3, fmt.Sprintf(format, args...))
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
