// Package testutil holds helpers shared by gonf's tests. It is only imported
// from _test.go files; it lives in a regular file so that several test
// packages (api, internal/cli) can share it.
package testutil

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// DirSnapshot is a deep fingerprint of a directory tree: for every entry its
// type, permission bits, size, modification time and content (regular files) or
// link target (symlinks), the tree root included. A tree that does not exist
// snapshots as Absent, so "was not created" is comparable too.
type DirSnapshot struct {
	Absent  bool
	Entries map[string]string // slash-relative path ("." = root) -> fingerprint
}

// Snapshot fingerprints dir without following symlinks or touching anything
// (it only reads, so it cannot change the mtimes it records).
func Snapshot(t testing.TB, dir string) DirSnapshot {
	t.Helper()
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return DirSnapshot{Absent: true}
	}
	snap := DirSnapshot{Entries: map[string]string{}}
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		fp := fmt.Sprintf("%s perm=%o size=%d mtime=%d", info.Mode().Type(), info.Mode().Perm(), info.Size(), info.ModTime().UnixNano())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fp += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fp += " content=" + describeContent(data)
		}
		snap.Entries[filepath.ToSlash(rel)] = fp
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return snap
}

// describeContent renders small contents verbatim (readable failures) and
// large ones as a SHA-256, so a multi-hundred-KiB blob does not flood the
// failure output while still being compared byte for byte.
func describeContent(data []byte) string {
	if len(data) <= 200 {
		return fmt.Sprintf("%q", data)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// Diff describes how other differs from s (added, removed and changed
// entries, or a created/removed root); it is empty when both are identical.
func (s DirSnapshot) Diff(other DirSnapshot) string {
	if s.Absent != other.Absent {
		return fmt.Sprintf("root: absent=%v -> absent=%v", s.Absent, other.Absent)
	}
	var lines []string
	for path, fp := range s.Entries {
		got, ok := other.Entries[path]
		switch {
		case !ok:
			lines = append(lines, "removed: "+path)
		case got != fp:
			lines = append(lines, fmt.Sprintf("changed: %s\n  before: %s\n  after:  %s", path, fp, got))
		}
	}
	for path := range other.Entries {
		if _, ok := s.Entries[path]; !ok {
			lines = append(lines, "added: "+path)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// RequireUnchanged fails the test when dir no longer matches before: not a
// single entry may have been added, removed, rewritten or re-timestamped, and a
// directory that did not exist must still not exist.
func RequireUnchanged(t testing.TB, before DirSnapshot, dir string) {
	t.Helper()
	if diff := before.Diff(Snapshot(t, dir)); diff != "" {
		t.Fatalf("%s was modified:\n%s", dir, diff)
	}
}
