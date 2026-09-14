package dir

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestGlobMatchCounts pins the one glob-match rule shared by
// copySourceGlob, pruneGlob's keep-set, and plan's scanGlob: a match
// counts when it is a regular file or a symlink resolving to a regular
// file; directories, dangling links, symlinks to directories, and other
// non-regular entries do not count.
func TestGlobMatchCounts(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	symlink := func(name, target string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	write("file", "x")
	write("target", "t")
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink("tofile", "target")
	symlink("todir", "adir")
	symlink("dangling", "nowhere")
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}

	tests := []struct {
		name  string
		match string
		want  bool
	}{
		{"regular file counts", "file", true},
		{"directory does not count", "adir", false},
		{"symlink to regular file counts", "tofile", true},
		{"symlink to directory does not count", "todir", false},
		{"dangling symlink does not count", "dangling", false},
		{"fifo does not count", "fifo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := filepath.Join(root, tt.match)
			info, err := os.Lstat(match)
			if err != nil {
				t.Fatal(err)
			}
			if got := GlobMatchCounts(match, info); got != tt.want {
				t.Fatalf("GlobMatchCounts(%s) = %v, want %v", tt.match, got, tt.want)
			}
		})
	}
}
