package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// Every value-taking ssh option keeps its value, so the value is not read
// as the destination.
func TestParsePushArgsSSHOptionValues(t *testing.T) {
	for _, opt := range []string{"-b", "-c", "-e", "-i", "-l", "-m", "-o", "-p", "-B", "-D", "-E", "-F", "-I", "-J", "-L", "-O", "-P", "-Q", "-R", "-S", "-w", "-W"} {
		ssh, pos := parsePushArgs([]string{"--", opt, "val", "user@host", "task"})
		if len(ssh) != 2 || len(pos) != 2 || pos[0] != "user@host" {
			t.Errorf("%s: ssh=%q pos=%q", opt, ssh, pos)
		}
	}
}

// A pre-planted symlink at the sticky apply-dir is refused without changing
// the mode of the directory it points to.
func TestStickyApplyDirSymlinkTargetUntouched(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "victim")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "sticky")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareStickyApplyDir(link, true); err == nil {
		t.Fatal("symlinked apply-dir accepted")
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target mode changed to %o", fi.Mode().Perm())
	}
}
