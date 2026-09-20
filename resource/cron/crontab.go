package cron

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
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

const (
	crontabLockRoot    = "/tmp"
	crontabLockTimeout = 5 * time.Second
	crontabLockRetry   = 10 * time.Millisecond
)

// lockCrontab acquires a process-safe advisory lock for userName. The lock
// lives below a private directory owned by the target crontab UID, so a
// privileged apply and an apply by that account use the same lock. Another
// user cannot preseed or replace it in the shared /tmp namespace. Acquisition
// is bounded: a stuck Gonf process reports contention instead of waiting
// forever.
func lockCrontab(userName string) (func() error, error) {
	return lockCrontabWithin(userName, crontabLockTimeout)
}

func lockCrontabWithin(userName string, timeout time.Duration) (func() error, error) {
	return lockCrontabAt(userName, timeout, crontabLockRoot)
}

func lockCrontabAt(userName string, timeout time.Duration, root string) (func() error, error) {
	targetUID, err := crontabUserID(userName)
	if err != nil {
		return nil, err
	}
	return lockCrontabAtUID(userName, targetUID, timeout, root)
}

// lockCrontabAtUID is lockCrontabAt's testable core. Its namespace and
// ownership are the target crontab account's UID, rather than the caller's
// effective UID: root updating alice and alice updating her own crontab must
// contend on one lock.
func lockCrontabAtUID(userName string, targetUID uint32, timeout time.Duration, root string) (func() error, error) {
	dir, path := crontabLockPath(root, targetUID, userName)
	dirFD, err := openPrivateLockDir(dir, targetUID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()

	fd, err := openPrivateLockFile(dirFD, filepath.Base(path), targetUID)
	if err != nil {
		return nil, err
	}
	if err := flockWithin(fd, timeout); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("lock crontab for %s: %w", userName, err)
	}
	return func() error {
		unlockErr := unix.Flock(fd, unix.LOCK_UN)
		closeErr := unix.Close(fd)
		if unlockErr != nil {
			return fmt.Errorf("unlock crontab for %s: %w", userName, unlockErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close crontab lock: %w", closeErr)
		}
		return nil
	}, nil
}

func crontabUserID(userName string) (uint32, error) {
	u, err := user.Lookup(userName)
	if err != nil {
		return 0, fmt.Errorf("lookup crontab user %q: %w", userName, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse uid %q for crontab user %q: %w", u.Uid, userName, err)
	}
	return uint32(uid), nil
}

func crontabLockPath(root string, targetUID uint32, userName string) (string, string) {
	digest := sha256.Sum256([]byte(userName))
	dir := filepath.Join(root, fmt.Sprintf("gonf-crontab-%d", targetUID))
	return dir, filepath.Join(dir, fmt.Sprintf("%x.lock", digest[:]))
}

func openPrivateLockDir(path string, targetUID uint32) (int, error) {
	created := false
	if err := unix.Mkdir(path, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("create crontab lock directory: %w", err)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open crontab lock directory: %w", err)
	}
	if created {
		if err := unix.Fchmod(fd, 0o700); err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("set crontab lock directory mode: %w", err)
		}
		if err := chownLockObject(fd, targetUID); err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("set crontab lock directory owner: %w", err)
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("inspect crontab lock directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != targetUID || stat.Mode&0o777 != 0o700 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("unsafe crontab lock directory %s", path)
	}
	return fd, nil
}

func openPrivateLockFile(dirFD int, name string, targetUID uint32) (int, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return -1, fmt.Errorf("open crontab lock: %w", err)
	}
	if created {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("set crontab lock mode: %w", err)
		}
		if err := chownLockObject(fd, targetUID); err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("set crontab lock owner: %w", err)
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("inspect crontab lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != targetUID || stat.Mode&0o777 != 0o600 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("unsafe crontab lock file %s", name)
	}
	return fd, nil
}

// chownLockObject transfers a newly-created lock object only when necessary.
// A normal per-user apply already creates it as targetUID; a privileged apply
// creates it as root and must hand it to the crontab owner before accepting it.
func chownLockObject(fd int, targetUID uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect new lock object: %w", err)
	}
	if stat.Uid == targetUID {
		return nil
	}
	return unix.Fchown(fd, int(targetUID), -1)
}

func flockWithin(fd int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		time.Sleep(crontabLockRetry)
	}
}

// adoptLegacyCommand removes unmanaged cron entries whose parsed command is an
// exact match for legacyCommand. An empty legacyCommand opts out. Lines inside
// every valid Gonf block are protected, and any malformed Gonf marker disables
// adoption for this pass rather than guessing which lines are safe to remove.
func adoptLegacyCommand(current, legacyCommand string) (string, bool) {
	if legacyCommand == "" {
		return current, false
	}

	lines := splitKeep(current)
	protected, wellFormed := protectedGonfLines(lines)
	if !wellFormed {
		return current, false
	}

	out := make([]string, 0, len(lines))
	changed := false
	for i, line := range lines {
		command, isCronEntry := cronEntryCommand(line)
		if !protected[i] && isCronEntry && command == legacyCommand {
			changed = true
			continue
		}
		out = append(out, line)
	}
	if !changed {
		return current, false
	}
	return joinCrontabLines(out), true
}

// protectedGonfLines identifies every valid Gonf marker block. A malformed,
// nested, mismatched, or unclosed marker leaves the crontab untouched by
// legacy adoption: the parser cannot prove that a candidate is unmanaged.
func protectedGonfLines(lines []string) ([]bool, bool) {
	protected := make([]bool, len(lines))
	inBlock := false
	blockName := ""
	for i, line := range lines {
		kind, name, marker := gonfMarker(line)
		if !marker {
			if looksLikeGonfMarker(line) {
				return nil, false
			}
			if inBlock {
				protected[i] = true
			}
			continue
		}

		protected[i] = true
		switch kind {
		case markerBegin:
			if inBlock {
				return nil, false
			}
			inBlock = true
			blockName = name
		case markerEnd:
			if !inBlock || name != blockName {
				return nil, false
			}
			inBlock = false
			blockName = ""
		}
	}
	if inBlock {
		return nil, false
	}
	return protected, true
}

type markerKind uint8

const (
	markerBegin markerKind = iota + 1
	markerEnd
)

func gonfMarker(line string) (markerKind, string, bool) {
	line = strings.TrimSpace(line)
	for _, candidate := range []struct {
		prefix string
		kind   markerKind
	}{
		{beginMarkerPrefix, markerBegin},
		{endMarkerPrefix, markerEnd},
	} {
		if !strings.HasPrefix(line, candidate.prefix) || !strings.HasSuffix(line, "]") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(line, candidate.prefix), "]")
		if name == "" || strings.ContainsAny(name, " \t\n\r[]") {
			return 0, "", false
		}
		return candidate.kind, name, true
	}
	return 0, "", false
}

func looksLikeGonfMarker(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "# BEGIN GONF") || strings.HasPrefix(line, "# END GONF")
}

// cronEntryCommand returns the command portion of a portable five-field
// crontab entry. It deliberately rejects comments, @directives, environment
// assignments, and syntax outside the Linux/BSD cron subset. Legacy adoption
// must leave a line in place when it cannot prove that it is a cron entry.
func cronEntryCommand(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "@") {
		return "", false
	}
	i := 0
	fields := [5]string{}
	for field := 0; field < 5; field++ {
		for i < len(line) && unicode.IsSpace(rune(line[i])) {
			i++
		}
		start := i
		for i < len(line) && !unicode.IsSpace(rune(line[i])) {
			i++
		}
		if start == i {
			return "", false
		}
		fields[field] = line[start:i]
	}
	for field, value := range fields {
		if !validCronField(value, cronFieldRangeByIndex(field)) {
			return "", false
		}
	}
	for i < len(line) && unicode.IsSpace(rune(line[i])) {
		i++
	}
	if i == len(line) {
		return "", false
	}
	return line[i:], true
}

type cronRange struct {
	min   int
	max   int
	names map[string]int
}

func cronFieldRange(label string) cronRange {
	switch label {
	case "minute":
		return cronRange{min: 0, max: 59}
	case "hour":
		return cronRange{min: 0, max: 23}
	case "monthday":
		return cronRange{min: 1, max: 31}
	case "month":
		return cronRange{min: 1, max: 12, names: map[string]int{
			"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
			"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
		}}
	case "weekday":
		return cronRange{min: 0, max: 7, names: map[string]int{
			"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
		}}
	default:
		return cronRange{}
	}
}

func cronFieldRangeByIndex(field int) cronRange {
	return cronFieldRange([]string{"minute", "hour", "monthday", "month", "weekday"}[field])
}

func validCronField(value string, r cronRange) bool {
	if value == "" || strings.ContainsAny(value, " \t\n\r") {
		return false
	}
	for _, item := range strings.Split(value, ",") {
		if !validCronItem(item, r) {
			return false
		}
	}
	return true
}

func validCronItem(item string, r cronRange) bool {
	parts := strings.Split(item, "/")
	if len(parts) > 2 || parts[0] == "" {
		return false
	}
	if len(parts) == 2 {
		if !validPositiveDecimal(parts[1]) {
			return false
		}
	}
	base := parts[0]
	if base == "*" {
		return true
	}
	rangeParts := strings.Split(base, "-")
	if len(rangeParts) > 2 || rangeParts[0] == "" {
		return false
	}
	start, ok := cronFieldValue(rangeParts[0], r)
	if !ok {
		return false
	}
	if len(rangeParts) == 1 {
		return len(parts) == 1
	}
	end, ok := cronFieldValue(rangeParts[1], r)
	return ok && start <= end
}

func validPositiveDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	number, err := strconv.Atoi(value)
	return err == nil && number > 0
}

func cronFieldValue(value string, r cronRange) (int, bool) {
	if named, ok := r.names[strings.ToLower(value)]; ok {
		return named, true
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < r.min || number > r.max {
		return 0, false
	}
	return number, true
}

// mergeCrontab replaces or removes all named GONF blocks. desired empty → remove.
// An unclosed BEGIN for name is healed by dropping only that marker line (tail kept).
func mergeCrontab(current, name, desired string) (string, bool) {
	begin := beginMarker(name)
	end := endMarker(name)

	lines := splitKeep(current)
	var out []string
	found := 0
	healed := false
	i := 0
	for i < len(lines) {
		trim := strings.TrimSpace(lines[i])
		if trim != begin {
			out = append(out, lines[i])
			i++
			continue
		}
		endIdx := -1
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if t == begin {
				break
			}
			if t == end {
				endIdx = j
				break
			}
		}
		if endIdx < 0 {
			// Corrupt/unclosed marker: drop BEGIN only so we never wipe the crontab.
			found++
			healed = true
			i++
			continue
		}
		found++
		i = endIdx + 1
	}

	body := joinCrontabLines(out)

	if desired == "" {
		if found == 0 {
			return current, false
		}
		return body, true
	}

	if found == 1 && !healed {
		oldBlock := extractBlock(current, name)
		if oldBlock == desired {
			return current, false
		}
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
			if inBlock {
				// Nested BEGIN: abandon incomplete extract.
				return ""
			}
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

func joinCrontabLines(lines []string) string {
	body := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if body != "" {
		return body + "\n"
	}
	return ""
}
