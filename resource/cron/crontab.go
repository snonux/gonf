package cron

// crontab(1) client: reading and writing a user's whole crontab through the
// crontab command. The rest of the transaction lives beside it: locking in
// lock.go and lock_setup.go, merging in crontab_merge.go, marker parsing in
// crontab_markers.go and entry/field validation in crontab_fields.go.

import (
	"fmt"
	"os/user"
	"strings"

	"github.com/snonux/gonf/internal/exec"
)

func crontabArgs(userName string, extra ...string) []string {
	args := make([]string, 0, 2+len(extra))
	// Linux (and some BSDs) require privilege for -u even when targeting self.
	if cur, err := user.Current(); err == nil && cur.Username == userName {
		return append(args, extra...)
	}
	args = append(args, "-u", userName)
	return append(args, extra...)
}

// readCrontab returns c's user's crontab. A failure reports crontab's
// output sizes only (crontabFailure).
func (c *Cron) readCrontab() (string, error) {
	args := crontabArgs(c.user, "-l")
	stdout, stderr, code, err := c.runCrontabRead("crontab", args...)
	if err != nil {
		return "", fmt.Errorf("crontab %v: %w", args, err)
	}
	// Empty crontab: crontab -l typically exits 1 with "no crontab for".
	if code != 0 {
		msg := strings.ToLower(stdout + stderr)
		if strings.Contains(msg, "no crontab") {
			return "", nil
		}
		return "", crontabFailure(args, code, stdout, stderr)
	}
	return stdout, nil
}

// writeCrontab replaces c's user's crontab with content. A failure reports
// crontab's output sizes only (crontabFailure).
func (c *Cron) writeCrontab(content string) error {
	// crontab [-u USER] - reads from stdin on Linux/BSD.
	args := crontabArgs(c.user, "-")
	stdout, stderr, code, err := c.runCrontabWrite(content, "crontab", args...)
	if err != nil {
		return fmt.Errorf("crontab %v: %w", args, err)
	}
	if code != 0 {
		return crontabFailure(args, code, stdout, stderr)
	}
	return nil
}

// runCrontabRead runs crontab -l: c.readFn when injected, else the real
// internal/exec runner.
func (c *Cron) runCrontabRead(name string, args ...string) (string, string, int, error) {
	if c.readFn != nil {
		return c.readFn(name, args...)
	}
	return exec.Run(name, args...)
}

// runCrontabWrite runs crontab - with stdin as the new table: c.writeFn
// when injected, else the real internal/exec runner.
func (c *Cron) runCrontabWrite(stdin, name string, args ...string) (string, string, int, error) {
	if c.writeFn != nil {
		return c.writeFn(stdin, name, args...)
	}
	return exec.RunWithStdin(stdin, name, args...)
}

// crontabFailure reports a crontab run that exited code with the sizes of
// its output, never the output: crontab may quote the offending line or
// the whole table, and one table holds every job of the account, so even a
// job that is not sensitive itself may fail with another job's secret-
// bearing line in the output. That holds for every job, sensitive or not.
func crontabFailure(args []string, code int, stdout, stderr string) error {
	return fmt.Errorf("crontab %v failed (exit %d; output withheld: %d bytes stdout, %d bytes stderr; "+
		"crontab output may quote lines of a table holding other jobs' secrets)", args, code, len(stdout), len(stderr))
}
