package cron

import "testing"

// TestRunnerDefaults pins what a Cron built without injected runners
// (newCron) resolves its crontab runners and lock to: the real
// internal/exec runners, stdin reaching the command, and the real
// cross-process lock.
func TestRunnerDefaults(t *testing.T) {
	c := newCron("job", nil)
	if out, _, code, err := c.runCrontabRead("echo", "real"); err != nil || code != 0 || out != "real\n" {
		t.Fatalf("real crontab read runner = %q, %d, %v", out, code, err)
	}
	if out, _, code, err := c.runCrontabWrite("table\n", "cat"); err != nil || code != 0 || out != "table\n" {
		t.Fatalf("real crontab write runner = %q, %d, %v", out, code, err)
	}
	if c.inProcessLock {
		t.Fatal("a Cron without injected runners must take the cross-process lock")
	}
}
