package systemd

import "testing"

// TestRunCmdDefault pins that runCmd reaches the real runner when no
// testseam.FakeSystemctl fake is installed.
func TestRunCmdDefault(t *testing.T) {
	if out, _, code, err := runCmd("echo", "real"); err != nil || code != 0 || out != "real\n" {
		t.Fatalf("real runCmd = %q, %d, %v", out, code, err)
	}
}
