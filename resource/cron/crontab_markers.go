package cron

// Gonf block marker parsing: recognising the "# BEGIN GONF Cron[name]" and
// "# END GONF Cron[name]" lines (prefixes and builders live in cron.go) and
// mapping which crontab lines sit inside a valid block, so legacy adoption
// (crontab_merge.go) never deletes a managed line.

import "strings"

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
