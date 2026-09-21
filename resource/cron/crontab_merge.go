package cron

// Crontab text transformations: mergeCrontab replaces or removes the named
// Gonf block, and adoptLegacyCommand drops unmanaged entries that a Gonf
// block now owns. Both work on the text read by readCrontab (crontab.go)
// while Cron.apply holds the crontab write lock (lock.go); marker parsing
// lives in crontab_markers.go and entry parsing in crontab_fields.go.

import "strings"

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
