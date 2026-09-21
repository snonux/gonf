package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/plan"
)

// TestCLIVersionInfoFlags pins the informational-flag phase of runCLI: each
// flag prints exactly its value on stdout and exits 0, and when several are
// set the precedence is -version, then -plan-version, then
// -strict-preview-version (they also win over -list and task names).
func TestCLIVersionInfoFlags(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-version"}, internal.Version},
		{[]string{"-plan-version"}, fmt.Sprint(plan.CurrentVersion)},
		{[]string{"-strict-preview-version"}, fmt.Sprint(internal.StrictPreviewVersion)},
		{[]string{"-strict-preview-version", "-plan-version", "-version"}, internal.Version},
		{[]string{"-strict-preview-version", "-plan-version", "-list", "no-such-task"}, fmt.Sprint(plan.CurrentVersion)},
	}
	for _, tc := range cases {
		var code int
		var stderr string
		stdout := captureStdout(t, func() { code, stderr = runGonf(t, tc.args...) })
		if code != 0 || stdout != tc.want+"\n" || stderr != "" {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q; want 0, %q, empty", tc.args, code, stdout, stderr, tc.want+"\n")
		}
	}
}

// TestRunSubcommandRejectsTaskNames pins the ok=false fall-through that sends
// a non-subcommand name on to task execution (without running anything).
func TestRunSubcommandRejectsTaskNames(t *testing.T) {
	for _, name := range []string{"my_task", "Plan", "", "-list"} {
		if code, ok := runSubcommand(context.Background(), name, nil); ok || code != 0 {
			t.Fatalf("runSubcommand(%q) = %d, %v; want 0, false", name, code, ok)
		}
	}
}

// TestRunSubcommandAcceptsSubcommands pins the ok=true side for the
// subcommands no other test dispatches: dropping one of their cases would
// silently turn it into an "unknown task". Called without arguments, the
// dns-zone-* commands print usage and exit 2 without touching anything.
func TestRunSubcommandAcceptsSubcommands(t *testing.T) {
	for _, name := range []string{"dns-zone-serial", "dns-zone-equivalent"} {
		var code int
		var ok bool
		_ = captureStderr(t, func() { code, ok = runSubcommand(context.Background(), name, nil) })
		if !ok || code != 2 {
			t.Fatalf("runSubcommand(%q, nil) = %d, %v; want 2, true (usage)", name, code, ok)
		}
	}
}

// TestCLIUnknownTaskExitsOne pins the task phase's failure contract: exit 1
// and a single "error: ..." line on stderr.
func TestCLIUnknownTaskExitsOne(t *testing.T) {
	api.ResetTasks()
	t.Cleanup(api.ResetTasks)
	code, stderr := runGonf(t, "no_such_task_982")
	if code != 1 || !strings.HasPrefix(stderr, "error: ") || !strings.Contains(stderr, `"no_such_task_982"`) {
		t.Fatalf("exit %d, stderr %q; want 1 and an error: line naming the task", code, stderr)
	}
}
