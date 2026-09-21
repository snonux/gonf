package cron

import (
	"fmt"
	"os/user"
	"strconv"
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
// must leave a line in place when it cannot prove that it is a cron entry,
// because this parser decides which unmanaged lines get deleted.
//
// Fields are separated only by crontab(5) "blanks" (ASCII space and tab); see
// isCrontabBlank for why Unicode whitespace is deliberately not a separator.
func cronEntryCommand(line string) (string, bool) {
	i := skipCrontabBlanks(line, 0)
	if i == len(line) || line[i] == '#' || line[i] == '@' {
		return "", false
	}
	fields := [5]string{}
	for field := range fields {
		i = skipCrontabBlanks(line, i)
		start := i
		for i < len(line) && !isCrontabBlank(line[i]) {
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
	i = skipCrontabBlanks(line, i)
	if i == len(line) {
		return "", false
	}
	return line[i:], true
}

// isCrontabBlank reports whether b separates crontab fields. crontab(5) on
// Linux and the BSDs documents fields as separated by spaces or tabs, so only
// those two bytes qualify. This is intentionally byte-based and ASCII-only:
//
//   - Both bytes are below 0x80, and UTF-8 never uses a byte below 0x80
//     inside a multibyte sequence, so a split can never land mid-character
//     and non-ASCII text (valid or not) stays verbatim in fields and command.
//   - Unicode whitespace such as U+00A0 or U+2003, lone continuation bytes
//     like 0x85/0xA0 (which unicode.IsSpace(rune(b)) misreports as spaces),
//     and other ASCII controls such as \v are not separators. A line that
//     uses them either fails validCronField, or keeps them at the start of
//     its command so it cannot equal a legacy command that lacks them. Both
//     outcomes leave the line in place, the safe direction for adoption.
func isCrontabBlank(b byte) bool {
	return b == ' ' || b == '\t'
}

// skipCrontabBlanks returns the index of the first non-blank byte of line at
// or after i, or len(line) when only blanks remain.
func skipCrontabBlanks(line string, i int) int {
	for i < len(line) && isCrontabBlank(line[i]) {
		i++
	}
	return i
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
