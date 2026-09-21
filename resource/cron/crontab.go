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

func readCrontab(userName string) (string, error) {
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
		return "", fmt.Errorf("crontab %v failed (exit %d): %s%s", args, code, stdout, stderr)
	}
	return stdout, nil
}

func writeCrontab(userName, content string) error {
	// crontab [-u USER] - reads from stdin on Linux/BSD.
	args := crontabArgs(userName, "-")
	stdout, stderr, code, err := runCmdWithStdin(content, "crontab", args...)
	if err != nil {
		return fmt.Errorf("crontab %v: %w", args, err)
	}
	if code != 0 {
		return fmt.Errorf("crontab %v failed (exit %d): %s%s", args, code, stdout, stderr)
	}
	return nil
}
