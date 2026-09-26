package systemd

import (
	"slices"
	"strings"
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

// TestEnablementDisableOp pins how the is-enabled state maps to the
// operation that removes it. systemctl is-enabled exits 0 for static,
// indirect, generated, alias and transient units too, and disable exits 0
// for them without changing anything, so disabling them would report a
// change on every apply: they get no operation. A runtime enablement is
// only removed by disable --runtime. No printed state (a fake runner) with
// exit 0 is disabled plainly.
func TestEnablementDisableOp(t *testing.T) {
	tests := []struct {
		stdout      string
		code        int
		wantEnabled bool
		wantOp      []string
	}{
		{"enabled\n", 0, true, []string{"disable"}},
		{"enabled-runtime\n", 0, true, []string{"disable", "--runtime"}},
		{"static\n", 0, true, nil},
		{"indirect\n", 0, true, nil},
		{"generated\n", 0, true, nil},
		{"alias\n", 0, true, nil},
		{"transient\n", 0, true, nil},
		{"", 0, true, []string{"disable"}},
		{"disabled\n", 1, false, nil},
		{"masked\n", 1, false, nil},
		{"linked\n", 1, false, nil},
	}
	for _, tt := range tests {
		t.Run(strings.TrimSpace(tt.stdout), func(t *testing.T) {
			var saw []string
			c := NewClient(&runners.SystemdRunners{Run: func(name string, args ...string) (string, string, int, error) {
				saw = args
				return tt.stdout, "", tt.code, nil
			}})
			e, err := c.Enablement("x.service", true)
			if err != nil {
				t.Fatalf("Enablement: %v", err)
			}
			if want := []string{"--user", "is-enabled", "x.service"}; !slices.Equal(saw, want) {
				t.Errorf("argv = %v, want %v", saw, want)
			}
			if e.Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", e.Enabled, tt.wantEnabled)
			}
			if got := e.DisableOp(); !slices.Equal(got, tt.wantOp) {
				t.Errorf("DisableOp = %v, want %v", got, tt.wantOp)
			}
		})
	}
}
