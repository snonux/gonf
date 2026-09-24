package cli

import (
	"testing"
	"time"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
)

// xg2Preset is the value every setting holds before each case of
// TestCLISettingsScopedToInvocation: distinct from both the built-in
// defaults and every flag value the cases pass, so a restore to the default
// (instead of to the entry value) or a leaked flag value both show up.
var xg2Preset = cliSettings{
	dryRun:     false,
	logLevel:   logger.LevelError,
	privilege:  privilege.Sudo,
	profile:    "xg2-preset",
	cmdTimeout: 7 * time.Second,
}

// settingsScopeCase is one CLI invocation of
// TestCLISettingsScopedToInvocation. wantDuring, when non-nil, is what the
// xg2_probe task must observe while CLI() runs it.
type settingsScopeCase struct {
	name       string
	args       []string
	wantDuring *cliSettings
}

// settingsScopeCases covers each setting (log level, privilege mode,
// profile override, command timeout; dry-run rides along) on the success
// path and on the failure paths that return after configureCLI changed
// them: a usage error, a configureCLI error part-way through (-privilege is
// parsed after the timeout, the level and dry-run were set), an unknown
// task and a subcommand usage error.
func settingsScopeCases() []settingsScopeCase {
	flags := []string{"-verbose", "-privilege", "doas", "-profile", "rocky", "-cmd-timeout", "50ms"}
	with := func(tail ...string) []string {
		return append(append([]string{"gonf"}, flags...), tail...)
	}
	return []settingsScopeCase{
		{"all flags, task run", with("-n", "xg2_probe"), &cliSettings{
			dryRun: true, logLevel: logger.LevelDebug, privilege: privilege.Doas,
			profile: "rocky", cmdTimeout: 50 * time.Millisecond,
		}},
		// Unconditional settings reset to their defaults for the run; the
		// flag-only ones (profile, timeout) keep the entry value.
		{"no flags, task run", []string{"gonf", "xg2_probe"}, &cliSettings{
			dryRun: false, logLevel: logger.LevelInfo, privilege: privilege.None,
			profile: xg2Preset.profile, cmdTimeout: xg2Preset.cmdTimeout,
		}},
		{"-quiet usage error", []string{"gonf", "-quiet", "-profile", "rocky", "-cmd-timeout", "50ms"}, nil},
		{"bad -privilege", []string{"gonf", "-n", "-verbose", "-cmd-timeout", "50ms", "-privilege", "bogus", "xg2_probe"}, nil},
		{"unknown task", with("xg2_no_such_task"), nil},
		{"apply usage error", with("apply"), nil},
	}
}

// TestCLISettingsScopedToInvocation is task xg2's regression guard, the
// sibling of vg2's TestCLIDryRunScopedToInvocation: CLI() applies the log
// level, privilege mode, profile override and command timeout from its
// flags, and none of them may stay changed after it returns, whatever path
// it returns by. Before xg2 each stayed set, so e.g. a "-cmd-timeout 50ms"
// test left every later test's backend commands under a 50ms timeout.
func TestCLISettingsScopedToInvocation(t *testing.T) {
	orig := captureCLISettings()
	t.Cleanup(orig.restore)
	api.ResetInventory()
	api.ResetTasks()
	resource.ResetRepository()
	var during *cliSettings
	api.Task("xg2_probe", "", func() {
		s := captureCLISettings()
		during = &s
	})
	for _, tc := range settingsScopeCases() {
		xg2Preset.restore()
		during = nil
		setOSArgs(t, tc.args...)
		var code int
		_ = testutil.CaptureStderr(t, func() { code = CLI() })
		if got := captureCLISettings(); got != xg2Preset {
			t.Fatalf("%s (exit %d): settings after return = %+v, want the entry value %+v",
				tc.name, code, got, xg2Preset)
		}
		checkSettingsDuring(t, tc, code, during)
	}
}

// checkSettingsDuring fails the test unless the probe task saw tc's
// wantDuring while CLI() ran (a case without one must not have run it).
func checkSettingsDuring(t *testing.T, tc settingsScopeCase, code int, during *cliSettings) {
	t.Helper()
	switch {
	case tc.wantDuring == nil && during != nil:
		t.Fatalf("%s (exit %d): probe task ran, want a failure before it", tc.name, code)
	case tc.wantDuring == nil:
	case during == nil:
		t.Fatalf("%s (exit %d): probe task did not run", tc.name, code)
	case *during != *tc.wantDuring:
		t.Fatalf("%s: settings during the run = %+v, want %+v", tc.name, *during, *tc.wantDuring)
	}
}
