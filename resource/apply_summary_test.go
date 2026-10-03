package resource_test

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// TestPrintApplySummaryListsIDsOnlyWhenQuiet pins that the summary ending an
// apply does not repeat what the log already showed: at Info level and above
// every change logs its own line, so only the counts are printed; under
// -quiet (Warn) those lines are suppressed and the ids are listed.
// PrintSummary stays the complete report at every level.
func TestPrintApplySummaryListsIDsOnlyWhenQuiet(t *testing.T) {
	const counts = "summary: 1 ok, 1 changed, 0 skipped, 0 would-change\n"
	const ids = "  changed File[/lodge/motd]\n"
	for _, tc := range []struct {
		name  string
		level logger.Level
		want  string
	}{
		{name: "debug", level: logger.LevelDebug, want: counts},
		{name: "info", level: logger.LevelInfo, want: counts},
		{name: "quiet", level: logger.LevelWarn, want: counts + ids},
		{name: "error", level: logger.LevelError, want: counts + ids},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.CaptureLog(t, tc.level)
			resource.ResetReport()
			t.Cleanup(resource.ResetReport)
			resource.Note("File[/lodge/dam]", resource.StatusOK)
			resource.Note("File[/lodge/motd]", resource.StatusChanged)

			var apply, full strings.Builder
			resource.PrintApplySummary(&apply)
			if got := apply.String(); got != tc.want {
				t.Errorf("apply summary = %q, want %q", got, tc.want)
			}
			resource.PrintSummary(&full)
			if got := full.String(); got != counts+ids {
				t.Errorf("full summary = %q, want %q", got, counts+ids)
			}
		})
	}
}
