package cron

import "testing"

// TestRunnerDefaults pins what runCmd and runCmdWithStdin resolve to without
// a testseam fake: the real runners, stdin reaching the command.
func TestRunnerDefaults(t *testing.T) {
	if out, _, code, err := runCmd("echo", "real"); err != nil || code != 0 || out != "real\n" {
		t.Fatalf("real runCmd = %q, %d, %v", out, code, err)
	}
	if out, _, code, err := runCmdWithStdin("table\n", "cat"); err != nil || code != 0 || out != "table\n" {
		t.Fatalf("real runCmdWithStdin = %q, %d, %v", out, code, err)
	}
}
