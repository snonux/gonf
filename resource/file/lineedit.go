package file

// Line edits (WithLine(s)/WithoutLine(s)): accumulating the requested lines
// and applying them to the file's current content. The non-blocking read of
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
)

func (f *File) lineEdit() bool {
	return len(f.addLines) != 0 || len(f.removeLines) != 0
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

// resolveLine applies WithoutLine then WithLine to the on-disk file.
// noop is true when the file is missing and only removal was requested
// (already absent — nothing to write). A non-regular, non-symlink entry at
// the target (planted FIFO, socket, device node, directory) is a loud user
// error naming the path and the entry type — line edits manage regular
// files, and unlike the content path there is no "replace with new
// content" semantics to fall back on; see readForLineEdit.
func (f *File) resolveLine() (path string, content []byte, noop bool, err error) {
	path = f.path
	raw, readErr := readForLineEdit(path)
	if readErr != nil {
		// errors.Is, not os.IsNotExist: the raw os error is wrapped on the
		// way out of readForLineEdit, and os.IsNotExist does not unwrap %w
		// chains.
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", nil, false, readErr
		}
		if len(f.addLines) == 0 {
			return path, nil, true, nil
		}
		return path, []byte(strings.Join(f.addLines, "\n") + "\n"), false, nil
	}

	var kept []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if slices.Contains(f.removeLines, line) {
			continue
		}
		kept = append(kept, line)
	}
	if err := scanner.Err(); err != nil {
		return "", nil, false, fmt.Errorf("failed to scan file %s: %w", path, err)
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
