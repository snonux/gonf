package cron

// Crontab text transformations: mergeCrontab replaces or removes the named
// Gonf block, and adoptUnmanaged drops unmanaged entries that a Gonf block
// now owns. Both work on the text read by readCrontab (crontab.go) while
// Cron.apply holds the crontab write lock (lock.go); marker parsing lives in
// crontab_markers.go and entry parsing in crontab_fields.go.

import "strings"

// adoption selects the unmanaged crontab entries a present job takes over
// (removes) before its own block is written. Cron.adoption builds it.
//
// There are two independent matchers, and an entry matching either one is
// adopted:
//   - legacy (WithLegacyCommand): every entry whose parsed command equals
//     legacy exactly, whatever its schedule. This is the explicit opt-in for
//     a DIFFERENT old command line, or the same command on another schedule.
//   - identical (the default for every present job without WithCronEnv): an
//     entry that is the job itself, i.e. the same five schedule fields
//     (byte-equal after splitting on blanks, so "0  6" matches "0 6" but
//     "00 6" does not) and exactly the same command. Such a line would
//     otherwise keep running beside the managed block, so the job would run
//     twice. It is only adopted while no environment assignment follows it
//     in the table (see adoptUnmanaged): the managed block is appended at
//     the end, and moving the entry past a later NAME=value line would
//     change the environment it runs with.
type adoption struct {
	legacy    string
	identical *cronEntry
}

// cronEntry is one parsed crontab entry: its five schedule fields and its
// command.
type cronEntry struct {
	fields  [5]string
	command string
}

// adoptLegacyCommand removes unmanaged cron entries whose parsed command is
// an exact match for legacyCommand (adoption.legacy alone). An empty
// legacyCommand opts out.
func adoptLegacyCommand(current, legacyCommand string) (string, bool) {
	return adoptUnmanaged(current, adoption{legacy: legacyCommand})
}

// adoptUnmanaged removes the unmanaged cron entries a selects (see
// adoption). Only lines of the one crontab being rewritten are ever
// considered, so another user's crontab is never touched. Lines inside every
// valid Gonf block are protected, and any malformed Gonf marker disables
// adoption for this pass rather than guessing which lines are safe to remove.
func adoptUnmanaged(current string, a adoption) (string, bool) {
	if a.legacy == "" && a.identical == nil {
		return current, false
	}

	lines := splitKeep(current)
	protected, wellFormed := protectedGonfLines(lines)
	if !wellFormed {
		return current, false
	}
	lastEnv := lastEnvAssignment(lines)

	out := make([]string, 0, len(lines))
	changed := false
	for i, line := range lines {
		if !protected[i] && a.adopts(line, i > lastEnv) {
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

// adopts reports whether a takes over line. envStable says no environment
// assignment follows line in the table, which the identical matcher needs.
func (a adoption) adopts(line string, envStable bool) bool {
	fields, command, ok := cronEntryParts(line)
	if !ok {
		return false
	}
	if a.legacy != "" && command == a.legacy {
		return true
	}
	return a.identical != nil && envStable &&
		fields == a.identical.fields && command == a.identical.command
}

// lastEnvAssignment returns the index of the last line that may set a
// crontab environment variable (isCrontabEnvAssignment), protected Gonf
// block lines included since cron applies them to every later entry too, or
// -1 when there is none.
func lastEnvAssignment(lines []string) int {
	last := -1
	for i, line := range lines {
		if isCrontabEnvAssignment(line) {
			last = i
		}
	}
	return last
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
