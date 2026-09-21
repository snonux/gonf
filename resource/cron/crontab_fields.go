package cron

// Crontab entry and schedule-field validation for the portable Linux/BSD
// five-field subset: splitting an entry into its fields and command, and
// checking each field against its range and names. Cron's option validation
// (cron.go) checks its schedule with it, and legacy adoption
// (crontab_merge.go) relies on it to prove a line is a cron entry before it
// may delete it.

import (
	"strconv"
	"strings"
)

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
