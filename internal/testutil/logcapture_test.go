package testutil

import (
	"testing"

	"github.com/snonux/gonf/internal/logger"
)

// TestCaptureLog pins the capture: lines are recorded without timestamps at
// the requested level, lines above it are dropped, and the subtest's cleanup
// restores the previous level and stops recording.
func TestCaptureLog(t *testing.T) {
	logger.SetLevel(logger.LevelWarn)
	t.Cleanup(func() { logger.SetLevel(logger.LevelInfo) })
	var output func() string
	t.Run("capture", func(t *testing.T) {
		output = CaptureLog(t, logger.LevelInfo)
		logger.Info("hello %s", "world")
		logger.Debug("dropped")
	})
	if got := output(); got != "hello world\n" {
		t.Errorf("captured %q, want %q", got, "hello world\n")
	}
	if logger.GetLevel() != logger.LevelWarn {
		t.Errorf("level after cleanup = %v, want LevelWarn", logger.GetLevel())
	}
	logger.Warn("after cleanup")
	if got := output(); got != "hello world\n" {
		t.Errorf("capture kept recording after cleanup: %q", got)
	}
}
