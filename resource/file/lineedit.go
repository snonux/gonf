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
func (f *File) resolveLine() (path string, content []byte, noop bool, err error) {
	path = f.path
	lines, err := f.currentLines(path)
	if err != nil {
		return "", nil, false, err
	}
	if lines == nil && len(f.keyedLines) == 0 && len(f.addLines) == 0 {
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
	return path, []byte(strings.Join(kept, "\n") + "\n"), false, nil
}

// currentLines returns the file's lines, or nil (and no error) when the file
// does not exist. An existing empty file yields an empty, non-nil slice.
func (f *File) currentLines(path string) ([]string, error) {
	raw, err := readForLineEdit(path)
	if err != nil {
		// errors.Is, not os.IsNotExist: the raw os error is wrapped on the
		// way out of readForLineEdit, and os.IsNotExist does not unwrap %w
		// chains.
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	lines := []string{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan file %s: %w", path, err)
	}
	return lines, nil
}

// applyKeyedLine gives edit ownership of the lines starting with edit.Key:
// the first is replaced in place by edit.Line (so the setting keeps its
// position among comments and neighbouring settings), every further one is
// dropped, and edit.Line is appended when none exists. Replacing or dropping
// a differing line is logged, naming the key and counts but not the text
// (a line may hold a value the log must not repeat), so a legacy or
// administrator-set value being taken over is visible rather than silent.
func applyKeyedLine(path string, lines []string, edit resource.KeyedLine) []string {
	out := make([]string, 0, len(lines)+1)
	found := false
	replaced, dropped := 0, 0
	for _, line := range lines {
		if !strings.HasPrefix(line, edit.Key) {
			out = append(out, line)
			continue
		}
		if found {
			dropped++
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
	if replaced != 0 || dropped != 0 {
		logger.Info("file %s: keyed line %q replaces %d and drops %d existing line(s)", path, edit.Key, replaced, dropped)
	}
	return out
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
