package cron

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

// mergeCrontab replaces or removes the named GONF block. desired empty → remove.
func mergeCrontab(current, name, desired string) (string, bool) {
	begin := beginMarker(name)
	end := endMarker(name)

	lines := splitKeep(current)
	var out []string
	inBlock := false
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == begin {
			inBlock = true
			found = true
			continue
		}
		if inBlock {
			if strings.TrimSpace(line) == end {
				inBlock = false
			}
			continue
		}
		out = append(out, line)
	}

	body := strings.Join(out, "\n")
	body = strings.TrimRight(body, "\n")
	if body != "" {
		body += "\n"
	}

	if desired == "" {
		if !found {
			return current, false
		}
		return body, true
	}

	oldBlock := extractBlock(current, name)
	if oldBlock == desired {
		return current, false
	}

	return body + desired, true
}

func extractBlock(current, name string) string {
	begin := beginMarker(name)
	end := endMarker(name)
	lines := splitKeep(current)
	var b strings.Builder
	inBlock := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == begin {
			inBlock = true
			b.WriteString(begin)
			b.WriteByte('\n')
			continue
		}
		if inBlock {
			if trim == end {
				b.WriteString(end)
				b.WriteByte('\n')
				return b.String()
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return ""
}

func splitKeep(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
