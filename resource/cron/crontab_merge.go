package cron

// Crontab text transformations: mergeCrontab replaces (in place when it
// can, see mergeCrontab) or removes the named Gonf block, and adoptUnmanaged
// removes (or replaces in place with the job's block) unmanaged entries that
// a Gonf block now owns. Both work on the text read by readCrontab
// (crontab.go) while Cron.apply holds the crontab write lock (lock.go);
// marker parsing lives in crontab_markers.go and entry parsing in
// crontab_fields.go.

import (
	"slices"
	"strings"
)

// adoption selects the unmanaged crontab entries a present job takes over
// before its own block is written. Cron.adoption builds it.
//
// There are two independent matchers:
//   - legacy (WithLegacyCommand): every entry whose parsed command equals
//     legacy exactly, whatever its schedule, is removed; mergeCrontab then
//     appends the job's block at the end of the table if it has none yet
//     (an existing block is rewritten where it stands, see mergeCrontab).
//     This is the explicit opt-in for a DIFFERENT old command line, or the
//     same command on another schedule.
//   - identical (the default for every present job without WithCronEnv): an
//     entry that is the job itself, i.e. the same five schedule fields
//     (byte-equal after splitting on blanks, so "0  6" matches "0 6" but
//     "00 6" does not) and exactly the same command. Such a line would
//     otherwise keep running beside the managed block, so the job would run
//     twice. While the job has no block yet, the first identical entry is
//     REPLACED IN PLACE by block, so the job keeps exactly the environment
//     (the NAME=value lines above it, other Gonf blocks' WithCronEnv lines
//     included) it ran with; every further identical entry is a duplicate
//     run and is removed. Once the job has its block, every identical entry
//     is a duplicate of it and is removed.
//
// An entry matching the identical matcher is handled by it even when the
// legacy matcher matches too (WithLegacyCommand with the job's own
// command), since replacing it in place keeps its environment.
//
// Earlier versions refused identical adoption whenever any NAME=value line
// followed the entry, because the block was always appended at the end. A
// Gonf block with WithCronEnv (PATH=...) anywhere below the entry then left
// the old line in place and the job ran twice; in-place replacement removes
// the need for that guard.
type adoption struct {
	legacy    string
	identical *cronEntry
	// name and block are the job's name and its complete desired block
	// (Cron.block); both are set whenever identical is.
	name  string
	block string
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

// adoptUnmanaged removes (or, for the first identical entry of a job without
// a block, replaces with the job's block) the unmanaged cron entries a
// selects (see adoption). Only lines of the one crontab being rewritten are
// ever considered, so another user's crontab is never touched. Lines inside
// every valid Gonf block are protected, and any malformed Gonf marker
// disables adoption for this pass rather than guessing which lines are safe
// to remove.
func adoptUnmanaged(current string, a adoption) (string, bool) {
	if a.legacy == "" && a.identical == nil {
		return current, false
	}

	lines := splitKeep(current)
	protected, wellFormed := protectedGonfLines(lines)
	if !wellFormed {
		return current, false
	}
	// placed says the job's block is (or now will be) in the table, so a
	// further identical entry is only a duplicate to drop.
	placed := a.identical == nil || hasGonfBlock(lines, a.name)

	out := make([]string, 0, len(lines))
	changed := false
	for i, line := range lines {
		if protected[i] {
			out = append(out, line)
			continue
		}
		switch a.match(line) {
		case matchIdentical:
			changed = true
			if !placed {
				out = append(out, splitKeep(a.block)...)
				placed = true
			}
		case matchLegacy:
			changed = true
		default:
			out = append(out, line)
		}
	}
	if !changed {
		return current, false
	}
	return joinCrontabLines(out), true
}

// entryMatch says which adoption matcher (if any) took a crontab line.
type entryMatch uint8

const (
	matchNone entryMatch = iota
	matchIdentical
	matchLegacy
)

// match reports which matcher of a takes over line; identical wins over
// legacy (see adoption).
func (a adoption) match(line string) entryMatch {
	fields, command, ok := cronEntryParts(line)
	switch {
	case !ok:
		return matchNone
	case a.identical != nil && fields == a.identical.fields && command == a.identical.command:
		return matchIdentical
	case a.legacy != "" && command == a.legacy:
		return matchLegacy
	default:
		return matchNone
	}
}

// hasGonfBlock reports whether lines hold a BEGIN marker for the named job.
// adoptUnmanaged calls it only after protectedGonfLines proved every marker
// well-formed, so a BEGIN always has its END.
func hasGonfBlock(lines []string, name string) bool {
	for _, line := range lines {
		if kind, n, ok := gonfMarker(line); ok && kind == markerBegin && n == name {
			return true
		}
	}
	return false
}

// mergeCrontab replaces or removes all named GONF blocks. desired empty → remove.
// An unclosed BEGIN for name is healed by dropping only that marker line (tail kept).
//
// A changed block is rewritten where the first well-formed old block stood,
// not appended at the end: cron applies NAME=value lines (PATH=, MAILTO=,
// other blocks' WithCronEnv) to every entry below them, so moving the block
// down would silently change the job's environment. That holds only while
// the block's own environment lines are unchanged; if they differ, splicing
// in place would apply the new values to every line below the block, so the
// block is appended at the end instead (as it is when no closed block
// existed).
func mergeCrontab(current, name, desired string) (string, bool) {
	s := stripNamedBlocks(splitKeep(current), beginMarker(name), endMarker(name))

	if desired == "" {
		if s.found == 0 {
			return current, false
		}
		return joinCrontabLines(s.lines), true
	}

	if s.found == 1 && !s.healed && extractBlock(current, name) == desired {
		return current, false
	}
	desiredLines := splitKeep(desired)
	if s.at < 0 || !slices.Equal(s.firstEnv, blockEnvLines(desiredLines)) {
		return joinCrontabLines(s.lines) + desired, true
	}
	out := make([]string, 0, len(s.lines)+len(desiredLines))
	out = append(out, s.lines[:s.at]...)
	out = append(out, desiredLines...)
	out = append(out, s.lines[s.at:]...)
	return joinCrontabLines(out), true
}

// strippedCrontab is a crontab with every block of one job removed.
type strippedCrontab struct {
	lines    []string // remaining lines, in original order
	at       int      // index in lines where the first closed block stood; -1 if none
	firstEnv []string // blockEnvLines of that first closed block
	found    int      // closed blocks plus unclosed BEGIN markers removed
	healed   bool     // an unclosed BEGIN marker was dropped
}

// stripNamedBlocks removes every begin..end block from lines. An unclosed
// begin (no end before EOF or before the next begin) loses only its marker
// line, so a corrupt crontab is never wiped.
func stripNamedBlocks(lines []string, begin, end string) strippedCrontab {
	s := strippedCrontab{at: -1}
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) != begin {
			s.lines = append(s.lines, lines[i])
			i++
			continue
		}
		s.found++
		endIdx := findBlockEnd(lines, i+1, begin, end)
		if endIdx < 0 {
			s.healed = true
			i++
			continue
		}
		if s.at < 0 {
			s.at = len(s.lines)
			s.firstEnv = blockEnvLines(lines[i+1 : endIdx])
		}
		i = endIdx + 1
	}
	return s
}

// findBlockEnd returns the index of the first end marker at or after from,
// or -1 if a begin marker or the end of lines comes first.
func findBlockEnd(lines []string, from int, begin, end string) int {
	for j := from; j < len(lines); j++ {
		switch strings.TrimSpace(lines[j]) {
		case begin:
			return -1
		case end:
			return j
		}
	}
	return -1
}

// blockEnvLines returns the lines of a Gonf block that are neither blank,
// comments (markers included), nor cron entries: the WithCronEnv NAME=value
// lines Cron.block writes. Blank and comment lines do not affect the
// environment, so they never force a move. Any other line found in a
// hand-edited block (an @reboot entry, say) is kept, so it makes the
// comparison in mergeCrontab fail safe (append).
func blockEnvLines(lines []string) []string {
	var env []string
	for _, line := range lines {
		if rest := strings.TrimLeft(line, " \t"); rest == "" || rest[0] == '#' {
			continue
		}
		if _, _, entry := cronEntryParts(line); entry {
			continue
		}
		env = append(env, line)
	}
	return env
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
