package file

// Line edits (WithLine(s)/WithoutLine(s)/WithKeyedLine): accumulating the
// requested lines and applying them to the file's current content. The non-blocking read of
// that content lives in read.go; the write goes through ensureFile
// (checksum.go) like every other content change.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
)

func (f *File) lineEdit() bool {
	return len(f.addLines) != 0 || len(f.removeLines) != 0 || len(f.keyedLines) != 0
}

func appendUniqueLines(dst []string, lines ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(lines))
	for _, line := range dst {
		seen[line] = struct{}{}
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		dst = append(dst, line)
	}
	return dst
}

// resolveLine applies WithoutLine, then WithKeyedLine, then WithLine to the
// on-disk file, in that order. noop is true when the file is missing and
// only removal was requested (already absent — nothing to write); a missing
// file with keyed or added lines is created holding exactly those lines. A
// non-regular, non-symlink entry at the target (planted FIFO, socket, device
// node, directory) is a loud user error naming the path and the entry type
// — line edits manage regular files, and unlike the content path there is no
// "replace with new content" semantics to fall back on; see readForLineEdit.
//
// The rebuilt content is joined with term, the file's own dominant line
// terminator (see currentLines/dominantTerminator), instead of a hard-coded
// "\n": otherwise a CRLF-terminated shared file would be silently rewritten
// to LF by any keyed/added/removed-line edit, breaking
// docs/file-dir-link.md's "every other line ... left alone" promise for line
// endings too (task bc2 finding (a)). A brand-new file (missing before this
// edit) is written with "\n", matching the prior behavior.
func (f *File) resolveLine() (path string, content []byte, noop bool, err error) {
	path = f.path
	lines, term, exists, err := f.currentLines(path)
	if err != nil {
		return "", nil, false, err
	}
	if !exists && len(f.keyedLines) == 0 && len(f.addLines) == 0 {
		return path, nil, true, nil
	}

	kept := make([]string, 0, len(lines)+len(f.keyedLines)+len(f.addLines))
	for _, line := range lines {
		if !slices.Contains(f.removeLines, line) {
			kept = append(kept, line)
		}
	}
	for _, edit := range f.keyedLines {
		kept = applyKeyedLine(path, kept, edit)
	}
	for _, add := range f.addLines {
		if !slices.Contains(kept, add) {
			kept = append(kept, add)
		}
	}

	if len(kept) == 0 {
		return path, []byte{}, false, nil
	}
	return path, []byte(strings.Join(kept, term) + term), false, nil
}

// currentLines returns the file's lines, its dominant line terminator (see
// dominantTerminator), and whether the file exists at all. exists is an
// explicit signal instead of the former nil-vs-empty-slice convention on
// lines alone (missing file: nil; existing empty file: an empty non-nil
// slice) that 100 Go Mistakes #22 flags as fragile — a later edit could
// collapse that distinction (e.g. by initializing lines to []string{} up
// front) without any test noticing, since both a nil and an empty slice
// range over zero elements. This is a pure internal refactor: resolveLine's
// noop check observes the same behavior as before, just through exists
// instead of a `lines == nil` comparison.
func (f *File) currentLines(path string) (lines []string, term string, exists bool, err error) {
	raw, err := readForLineEdit(path)
	if err != nil {
		// errors.Is, not os.IsNotExist: the raw os error is wrapped on the
		// way out of readForLineEdit, and os.IsNotExist does not unwrap %w
		// chains.
		if errors.Is(err, os.ErrNotExist) {
			return nil, "\n", false, nil
		}
		return nil, "", false, err
	}
	lines = []string{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, "", false, fmt.Errorf("failed to scan file %s: %w", path, err)
	}
	return lines, dominantTerminator(raw), true, nil
}

// dominantTerminator reports the line terminator raw predominantly uses:
// "\r\n" when raw has strictly more CRLF line endings than bare LF ones,
// else "\n". A bare LF is one not immediately preceded by "\r" (bufio's
// ScanLines already strips the CR from a CRLF line when currentLines scans
// raw, so nothing downstream of this needs to know which terminator any
// individual line originally had — the whole file is written back with one,
// uniform terminator). A file with no line breaks, or with equally many of
// each, keeps "\n". A mixed-ending file's minority-style lines are therefore
// normalized to the majority style, same as a homogeneously-terminated file
// keeps its own terminator; see docs/file-dir-link.md's keyed-line section.
func dominantTerminator(raw []byte) string {
	crlf := bytes.Count(raw, []byte("\r\n"))
	lf := bytes.Count(raw, []byte("\n")) - crlf
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}

// applyKeyedLine gives edit ownership of the lines starting with edit.Key:
// the first is replaced in place by edit.Line (so the setting keeps its
// position among comments and neighbouring settings), every further one is
// dropped, and edit.Line is appended when none exists.
//
// The match tolerates leading whitespace: a line is owned when edit.Key
// prefixes it after stripping leading spaces/tabs (strings.TrimLeft(line, "
// \t")), so an administrator's indented hand edit, or a line accidentally
// indented by an earlier tool, is still recognized instead of being left
// alone beside a newly appended, conflicting line (task bc2 finding (b)).
// The match stays literal otherwise: extra *internal* whitespace (e.g.
// "export  PKG_PATH=old" with two spaces) and case differences (e.g.
// "EXPORT PKG_PATH=old") are NOT tolerated and are therefore left in place,
// untouched, with edit.Line appended as a second, conflicting line — safely
// normalizing those would need real parsing (word-splitting, case folding a
// key that might itself be case-sensitive shell syntax) this feature does
// not attempt; see docs/file-dir-link.md's keyed-line section, which
// documents the gap. An owned, indented line is replaced by edit.Line
// unindented: the key now owns the line's position, not its original
// indentation.
//
// A plain replace (the owned line's value converging to a new one) is the
// common, expected case and stays at Info. A drop is different: it deletes
// a line the key did not narrowly target — declaration-time validation
// (validateKeyedLines) only checks that key/line are shaped correctly, not
// that key is specific enough for THIS file's actual content, so a key that
// is accidentally too broad (e.g. "export " instead of "export
// PKG_PATH=") silently destroys unrelated administrator-written lines that
// merely happen to share the prefix. That must be visible even under
// `-quiet` (which lowers the level to Warn, see internal/logger, so Info
// and below are suppressed), so a drop is logged at Warn instead, naming the
// key and the counts but never the dropped text: a line may hold a value
// that is not necessarily marked sensitive the way a secret-bearing op is,
// so it would not go through gonf's redaction. The dropped lines' text is
// only ever logged at Debug, for local recoverability, never at Warn or
// Info.
//
// The drop is not counted or surfaced separately from an ordinary edit:
// like WithLine/WithoutLine, it relies on ensureFile's checksum comparison
// against the file's previous content to note the resource StatusChanged
// (a drop always changes the produced content, since it removes a line),
// which is how it reaches the apply summary ("N changed") that an operator
// reviewing output actually reads, rather than only the log.
func applyKeyedLine(path string, lines []string, edit resource.KeyedLine) []string {
	out := make([]string, 0, len(lines)+1)
	var droppedLines []string
	found := false
	replaced, dropped := 0, 0
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimLeft(line, " \t"), edit.Key) {
			out = append(out, line)
			continue
		}
		if found {
			dropped++
			droppedLines = append(droppedLines, line)
			continue
		}
		found = true
		if line != edit.Line {
			replaced++
		}
		out = append(out, edit.Line)
	}
	if !found {
		out = append(out, edit.Line)
	}
	logKeyedLineResult(path, edit.Key, replaced, dropped, droppedLines)
	return out
}

// logKeyedLineResult reports applyKeyedLine's outcome, phrased by mode:
// resolveLine (and therefore applyKeyedLine) runs before ensureFile's
// dry-run gating (checksum.go), so under `gonf -n` nothing downstream of
// this call actually writes the file — logging the present tense there
// would claim a change dry-run never makes (task bc2 finding (c)). This
// follows the same present/"would ..." distinction resource.Mutate's
// dryRunPrefix draws for every other resource kind's mutation, without
// going through Mutate itself: a keyed-line drop/replace is not gated as
// its own mutation, only as part of the file's eventual content write.
//
// A drop logs at Warn (visible even under -quiet) naming the key and counts
// but never the dropped text (see applyKeyedLine's doc comment); a plain
// replace logs at Info; an edit that changed nothing (already converged, or
// a fresh append with nothing to report) logs nothing.
func logKeyedLineResult(path, key string, replaced, dropped int, droppedLines []string) {
	verb, dropVerb := "replaces", "drops"
	if resource.DryRun() {
		verb, dropVerb = "would replace", "would drop"
	}
	switch {
	case dropped != 0:
		logger.Warn("file %s: keyed line %q %s %d and %s %d existing line(s); a drop deletes content the key matched but did not own — check whether %q is narrow enough for this file", path, key, verb, replaced, dropVerb, dropped, key)
		logger.Debug("file %s: keyed line %q dropped line(s): %q", path, key, droppedLines)
	case replaced != 0:
		logger.Info("file %s: keyed line %q %s %d existing line(s)", path, key, verb, replaced)
	}
}

// validateKeyedLines refuses WithKeyedLine declarations whose ownership is
// ambiguous or cannot converge (see opt.WithKeyedLine): an empty key, a line
// not starting with its key or spanning lines, one key declared with two
// different lines or prefixing another key, and a WithLine/WithoutLine line
// the key would also own.
func (f *File) validateKeyedLines(path string) error {
	for i, edit := range f.keyedLines {
		if err := validateKeyedLine(edit); err != nil {
			return fmt.Errorf("file %s: %w", path, err)
		}
		for _, other := range f.keyedLines[i+1:] {
			if other.Key == edit.Key {
				return fmt.Errorf("file %s: WithKeyedLine %q is declared with two different lines", path, edit.Key)
			}
			if strings.HasPrefix(other.Key, edit.Key) || strings.HasPrefix(edit.Key, other.Key) {
				return fmt.Errorf("file %s: WithKeyedLine keys %q and %q overlap; one line must have one owner", path, edit.Key, other.Key)
			}
		}
		for _, line := range slices.Concat(f.addLines, f.removeLines) {
			if strings.HasPrefix(line, edit.Key) {
				return fmt.Errorf("file %s: WithLine/WithoutLine %q starts with WithKeyedLine key %q, which already owns it", path, line, edit.Key)
			}
		}
	}
	return nil
}

func validateKeyedLine(edit resource.KeyedLine) error {
	switch {
	case edit.Key == "":
		return fmt.Errorf("WithKeyedLine requires a non-empty key")
	case strings.ContainsAny(edit.Line, "\r\n"):
		return fmt.Errorf("WithKeyedLine %q: the line must not contain a line break", edit.Key)
	case !strings.HasPrefix(edit.Line, edit.Key):
		return fmt.Errorf("WithKeyedLine %q: the line must start with its key, or the edit never converges", edit.Key)
	}
	return nil
}
