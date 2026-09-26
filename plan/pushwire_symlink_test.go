package plan

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symlink entry followed by a regular entry at the same path must not write
// through the symlink outside planDir.
func TestExtractTarFinalSymlinkRefused(t *testing.T) {
	planDir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	var written int64
	link := &tar.Header{Name: "blobs/x", Typeflag: tar.TypeSymlink, Linkname: victim}
	if err := extractTarHeader(planDir, link, strings.NewReader(""), &written, 1<<20); err != nil {
		t.Fatal(err)
	}
	file := &tar.Header{Name: "blobs/x", Typeflag: tar.TypeReg, Size: 5}
	if err := extractTarHeader(planDir, file, strings.NewReader("PWNED"), &written, 1<<20); err == nil {
		t.Fatal("regular entry written through a symlink")
	}
	if got, _ := os.ReadFile(victim); string(got) != "safe" {
		t.Fatalf("victim overwritten: %q", got)
	}
}
