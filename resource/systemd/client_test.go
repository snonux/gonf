package systemd

import (
	"testing"

	"github.com/snonux/gonf/internal/runners"
)

// TestRunCmdDefault pins that a Client with no injected runner reaches the
// real internal/exec runner.
func TestRunCmdDefault(t *testing.T) {
	if out, _, code, err := (Client{}).runCmd("echo", "real"); err != nil || code != 0 || out != "real\n" {
		t.Fatalf("real runCmd = %q, %d, %v", out, code, err)
	}
}

// TestNewClientInjectsRunner pins NewClient's contract: a nil
// *runners.SystemdRunners (or one with a nil Run field) yields the real
// runner, and a non-nil Run field is the runner every helper on the Client
// consults.
func TestNewClientInjectsRunner(t *testing.T) {
	if c := NewClient(nil); c.run != nil {
		t.Fatal("NewClient(nil) carries a runner")
	}
	if c := NewClient(&runners.SystemdRunners{}); c.run != nil {
		t.Fatal("NewClient with a nil Run field carries a runner")
	}
	var saw []string
	fake := func(name string, args ...string) (string, string, int, error) {
		saw = append(saw, name)
		return "", "", 0, nil
	}
	c := NewClient(&runners.SystemdRunners{Run: fake})
	if _, _, _, err := c.runCmd("systemctl", "daemon-reload"); err != nil {
		t.Fatalf("runCmd: %v", err)
	}
	if len(saw) != 1 || saw[0] != "systemctl" {
		t.Fatalf("injected runner not reached: %v", saw)
	}
}
