package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestReadPrivateFileRoundTrip: what WritePrivateFile wrote reads back byte
// for byte, also from a relative directory.
func TestReadPrivateFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// t.TempDir is 0777 minus the umask; WritePrivateFile would refuse it
	// under umask 000.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte("{\"op\":\"plan\"}\n\x00tail")
	if err := WritePrivateFile(dir, "plan.jsonl", want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPrivateFile(dir, "plan.jsonl")
	if err != nil || string(got) != string(want) {
		t.Fatalf("ReadPrivateFile = %q, %v; want %q", got, err, want)
	}
	t.Chdir(dir)
	if got, err := ReadPrivateFile(".", "plan.jsonl"); err != nil || string(got) != string(want) {
		t.Fatalf("ReadPrivateFile(.) = %q, %v; want %q", got, err, want)
	}
}

// TestSplitFilePath: the split happens before any cleaning, so a trailing
// separator or a "." / ".." last element is never turned into a file name.
func TestSplitFilePath(t *testing.T) {
	for _, tc := range []struct {
		path, dir, name string
		ok              bool
	}{
		{"plan.jsonl", ".", "plan.jsonl", true},
		{"out/plan.jsonl", "out", "plan.jsonl", true},
		{"/plan.jsonl", "/", "plan.jsonl", true},
		{"a//b", "a/", "b", true},
		{"out/", "out", "", false},
		{"out/.", "out", ".", false},
		{"out/..", "out", "..", false},
		{".", ".", ".", false},
		{"/", "/", "", false},
		{"", ".", "", false},
	} {
		dir, name, ok := splitFilePath(tc.path)
		if dir != tc.dir || name != tc.name || ok != tc.ok {
			t.Errorf("splitFilePath(%q) = %q, %q, %v; want %q, %q, %v", tc.path, dir, name, ok, tc.dir, tc.name, tc.ok)
		}
	}
}

// TestReadPrivateFilePathRefusesDirectories: every spelling of a directory is
// refused with the same message, even when a file named like the directory
// sits inside it (what a Dir/Base split of "out/" would have read).
func TestReadPrivateFilePathRefusesDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "out", "out"), []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root + "/out/", root + "/out/.", root + "/out/..", ".", "/"} {
		want := "plan: " + path + " does not name a file; a directory is not a plan file"
		if data, err := ReadPrivateFilePath(path); err == nil || err.Error() != want {
			t.Errorf("ReadPrivateFilePath(%q) = %q, %v; want %q", path, data, err, want)
		}
	}
	if data, err := ReadPrivateFilePath(root + "/out/out"); err != nil || string(data) != "plan" {
		t.Fatalf("ReadPrivateFilePath(out/out) = %q, %v", data, err)
	}
}

// TestReadPrivateFileRefusals: the file is never read through a symlink
// (whether it points at a regular file or dangles), a FIFO or a directory is
// not a plan file, and a name that is not a single component is refused. Each
// error carries the "plan: " prefix exactly once.
func TestReadPrivateFileRefusals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real"), []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"link": "real", "dangling": "nowhere"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"link":     filepath.Join(dir, "link") + " is a symlink; refusing to read a plan through it",
		"dangling": filepath.Join(dir, "dangling") + " is a symlink; refusing to read a plan through it",
		"fifo":     filepath.Join(dir, "fifo") + " is not a regular file",
		"sub":      filepath.Join(dir, "sub") + " is not a regular file",
		"missing":  "open " + filepath.Join(dir, "missing") + ": no such file or directory",
		"a/b":      `invalid private file name "a/b"`,
		"..":       `invalid private file name ".."`,
		"":         `invalid private file name ""`,
	} {
		data, err := ReadPrivateFile(dir, name)
		if err == nil || err.Error() != "plan: "+want || data != nil {
			t.Errorf("ReadPrivateFile(%q) = %q, %v; want error %q", name, data, err, "plan: "+want)
		}
		if err != nil && strings.Count(err.Error(), "plan: ") != 1 {
			t.Errorf("ReadPrivateFile(%q) error %q repeats the package prefix", name, err)
		}
	}
}
