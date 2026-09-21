package plan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// blobWriters is every Store entry point that writes below blobs/, so the
// tests of the blobs/ policy cover all three the same way.
func blobWriters(t *testing.T) map[string]func(*Store) error {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]func(*Store) error{
		"WriteFile": func(s *Store) error { _, err := s.WriteFile("b", []byte("x")); return err },
		"WriteTree": func(s *Store) error { _, err := s.WriteTree("t", src); return err },
		"WriteGlob": func(s *Store) error { _, err := s.WriteGlob("g", filepath.Join(src, "*")); return err },
	}
}

// requireBlobRefusal checks the exact wording shape of a blob store refusal:
// ONE leading package prefix ("plan: ", never "plan: ... plan: ..."), the
// refused directory and the wanted reason in the text, and, through the Refusal
// interface, the same message without the prefix, which is what RecordPlan
// shows behind its own. It does not look at OS error text (which differs per
// platform), only at wording this package composes.
func requireBlobRefusal(t *testing.T, err error, wantParts ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("got nil, want a refusal")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "plan: ") || strings.Count(msg, "plan:") != 1 {
		t.Fatalf("refusal %q, want exactly one leading %q and no repeated package prefix", msg, "plan: ")
	}
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Reason() != strings.TrimPrefix(msg, "plan: ") {
		t.Fatalf("refusal %q is not a Refusal whose Reason is the message without %q", msg, "plan: ")
	}
	for _, part := range wantParts {
		if !strings.Contains(msg, part) {
			t.Fatalf("refusal %q, want it to contain %q", msg, part)
		}
	}
}

// TestStoreRefusesSymlinkedBlobsDir: a blobs/ that is a symlink (to a directory
// somewhere else) is refused by every writer, because every component of the
// path is opened O_NOFOLLOW, and nothing is written, cleared or changed
// through the link: the link's target keeps its content and its mode. The
// refusal names the blobs component; which errno the kernel reports for it
// (ENOTDIR on Linux, ELOOP or EMLINK elsewhere) is not asserted.
func TestStoreRefusesSymlinkedBlobsDir(t *testing.T) {
	for name, write := range blobWriters(t) {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			target := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
			if err := os.WriteFile(filepath.Join(target, "keep"), []byte("precious"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, "blobs")); err != nil {
				t.Fatal(err)
			}
			targetBefore, rootBefore := testutil.Snapshot(t, target), testutil.Snapshot(t, root)
			requireBlobRefusal(t, write(NewStore(root)), `component "blobs"`)
			testutil.RequireUnchanged(t, targetBefore, target)
			testutil.RequireUnchanged(t, rootBefore, root)
		})
	}
}

// TestWritePrivateFileReplacesSymlinkedPlanFile: a plan.jsonl that is a symlink
// is replaced by the rename of a fresh 0600 file, never written through, so the
// file the link pointed to keeps its content and its mode.
func TestWritePrivateFileReplacesSymlinkedPlanFile(t *testing.T) {
	dir := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "out"), 0o700)
	target := filepath.Join(t.TempDir(), "precious")
	if err := os.WriteFile(target, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "plan.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(dir, "plan.jsonl", []byte("new plan")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan.jsonl after the write: %v, %v; want a regular 0600 file, no symlink", info, err)
	}
	if got, err := os.ReadFile(link); err != nil || string(got) != "new plan" {
		t.Fatalf("plan.jsonl = %q, %v; want the new plan", got, err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "precious" {
		t.Fatalf("symlink target = %q, %v; want it untouched", got, err)
	}
	if got := modeOf(t, target); got != 0o644 {
		t.Fatalf("symlink target mode = %v, want 0644 untouched", got)
	}
}

// TestStoreBlobsDirGroupRule: the blobs/ policy is the shared directory rule,
// for every writer. An existing blobs/ that is group-writable by the caller's
// private group is kept as it is (a UPG checkout may be 0775 all the way
// down), while one that is group-writable by another group is refused before
// anything is cleared or written.
func TestStoreBlobsDirGroupRule(t *testing.T) {
	for name, write := range blobWriters(t) {
		t.Run(name+" private group is kept", func(t *testing.T) {
			testutil.RequirePrivateGroupUser(t)
			root := t.TempDir()
			mkdirMode(t, filepath.Join(root, "blobs"), 0o775)
			if err := write(NewStore(root)); err != nil {
				t.Fatalf("%s into a 0775 blobs/ of the private group = %v, want success", name, err)
			}
			if got := modeOf(t, filepath.Join(root, "blobs")); got != 0o775 {
				t.Fatalf("blobs mode = %v, want 0775 unchanged", got)
			}
		})
		t.Run(name+" shared group is refused", func(t *testing.T) {
			root := t.TempDir()
			sharedGroupDir(t, filepath.Join(root, "blobs"), 0o775)
			requireBlobRefusal(t, write(NewStore(root)), filepath.Join(root, "blobs"), "group-writable by group")
			requireEntries(t, filepath.Join(root, "blobs"))
		})
	}
}
