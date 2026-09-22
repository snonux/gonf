package logger

import (
	"strings"
	"testing"
)

// TestRedirectUnprefixed pins the redirect internal/testutil.CaptureLog
// builds on: output is written without timestamps at the requested level,
// lines above it are dropped, and restore reinstates the previous
// destination and level.
func TestRedirectUnprefixed(t *testing.T) {
	SetLevel(LevelWarn)
	t.Cleanup(func() { SetLevel(LevelInfo) })
	var buf strings.Builder
	restore := RedirectUnprefixed(&buf, LevelInfo)
	output := buf.String
	Info("hello %s", "world")
	Debug("dropped")
	restore()
	if got := output(); got != "hello world\n" {
		t.Errorf("captured %q, want %q", got, "hello world\n")
	}
	if GetLevel() != LevelWarn {
		t.Errorf("level after restore = %v, want LevelWarn", GetLevel())
	}
	Info("after restore")
	if got := output(); got != "hello world\n" {
		t.Errorf("capture kept writing after restore: %q", got)
	}
}
