package cli

import (
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal/clihost"
	"github.com/snonux/gonf/resource"
)

// TestCLIMarksHostOnlyWhileRunning pins the elevated re-exec guard's marker:
// it is set while CLI() runs (a task body sees it) and cleared once CLI()
// returns, so a main that calls cli.CLI() and then api.Apply with sudo/doas
// is refused instead of re-executing its own main as root.
func TestCLIMarksHostOnlyWhileRunning(t *testing.T) {
	t.Cleanup(clihost.SetForTest(false))
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(api.ResetTasks)
	var during bool
	api.Task("cli_marker", "observe the CLI host marker", func() { during = clihost.Active() })

	setOSArgs(t, "gonf", "plan", "-stdout", "cli_marker")
	if code := CLI(); code != 0 {
		t.Fatalf("plan exit %d", code)
	}
	if !during {
		t.Fatal("clihost.Active() = false inside a task body run by CLI()")
	}
	if clihost.Active() {
		t.Fatal("clihost.Active() = true after CLI() returned")
	}
}
