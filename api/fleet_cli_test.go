package api

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/snonux/gonf/resource"
)

func TestCLIFleet(t *testing.T) {
	ResetInventory()
	ResetTasks()
	resource.ResetRepository()
	Task("cli_fleet_task", "", func() {})
	Fleet("cli_fleet", Host("cli_h", WithSSHHost("cli.example")))

	old := sshRunner
	t.Cleanup(func() { sshRunner = old })
	var saw int
	sshRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
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
	ResetInventory()
	Host("list_h", WithSSHUser("u"), WithSSHHost("h.example"), WithSSHPort(22))
	Fleet("list_f", MustHost("list_h"))

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
