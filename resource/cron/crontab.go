package cron

// crontab(1) client: reading and writing a user's whole crontab through the
// crontab command. The rest of the transaction lives beside it: locking in
// lock.go and lock_setup.go, merging in crontab_merge.go, marker parsing in
// crontab_markers.go and entry/field validation in crontab_fields.go.

import (
	"fmt"
	"os/user"
	"strings"
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

// readCrontab returns userName's crontab. withhold (a sensitive Cron, see
// Cron.Sensitive) replaces crontab's output in a failure by its sizes.
func readCrontab(userName string, withhold bool) (string, error) {
	args := crontabArgs(userName, "-l")
	stdout, stderr, code, err := runCmd("crontab", args...)
	if err != nil {
		return "", fmt.Errorf("crontab %v: %w", args, err)
	}
	// Empty crontab: crontab -l typically exits 1 with "no crontab for".
	if code != 0 {
		msg := strings.ToLower(stdout + stderr)
		if strings.Contains(msg, "no crontab") {
			return "", nil
		}
		return "", crontabFailure(args, code, stdout, stderr, withhold)
	}
	return stdout, nil
}

// writeCrontab replaces userName's crontab with content; withhold is as for
// readCrontab.
func writeCrontab(userName, content string, withhold bool) error {
	// crontab [-u USER] - reads from stdin on Linux/BSD.
	args := crontabArgs(userName, "-")
	stdout, stderr, code, err := runCmdWithStdin(content, "crontab", args...)
	if err != nil {
		return fmt.Errorf("crontab %v: %w", args, err)
	}
	if code != 0 {
		return crontabFailure(args, code, stdout, stderr, withhold)
	}
	return nil
}

// crontabFailure reports a crontab run that exited code. crontab may quote
// the offending line (or the whole table) in its output, so with withhold
// the error carries only the output sizes.
func crontabFailure(args []string, code int, stdout, stderr string, withhold bool) error {
	if withhold {
		return fmt.Errorf("crontab %v failed (exit %d; output withheld: %d bytes stdout, %d bytes stderr; the cron job carries secret material)",
			args, code, len(stdout), len(stderr))
	}
	return fmt.Errorf("crontab %v failed (exit %d): %s%s", args, code, stdout, stderr)
}
