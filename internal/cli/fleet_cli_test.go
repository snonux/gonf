package cli

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/resource"
)

func TestCLIFleet(t *testing.T) {
	api.ResetInventory()
	api.ResetTasks()
	resource.ResetRepository()
	api.Task("cli_fleet_task", "", func() {})
	api.Fleet("cli_fleet", api.Host("cli_h", api.WithSSHHost("cli.example")))

	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })
	var saw int
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		saw++
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "fleet", "-n", "cli_fleet", "cli_fleet_task"}
	if code := CLI(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if saw != 1 {
		t.Fatalf("saw=%d", saw)
	}
}

func TestCLIHostsFleets(t *testing.T) {
	api.ResetInventory()
	api.Host("list_h", api.WithSSHUser("u"), api.WithSSHHost("h.example"), api.WithSSHPort(22))
	api.Fleet("list_f", api.MustHost("list_h"))

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"gonf", "hosts"}
	if code := CLI(); code != 0 {
		t.Fatalf("hosts exit %d", code)
	}
	os.Args = []string{"gonf", "fleets"}
	if code := CLI(); code != 0 {
		t.Fatalf("fleets exit %d", code)
	}
}
