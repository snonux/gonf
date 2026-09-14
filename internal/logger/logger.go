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
	std.Output(3, fmt.Sprintf(format, args...))
}

// Error logs a formatted message at LevelError.
func Error(format string, args ...any) { logf(LevelError, format, args...) }

// Warn logs a formatted message at LevelWarn.
func Warn(format string, args ...any) { logf(LevelWarn, format, args...) }

// Info logs a formatted message at LevelInfo.
func Info(format string, args ...any) { logf(LevelInfo, format, args...) }

// Debug logs a formatted message at LevelDebug.
func Debug(format string, args ...any) { logf(LevelDebug, format, args...) }

// Fatal logs at Error level and exits the process.
func Fatal(format string, args ...any) {
	logf(LevelError, format, args...)
	os.Exit(1)
}
