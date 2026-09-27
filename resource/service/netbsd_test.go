package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/atomicfile"
)

// netbsdEnableBackend is a NetBSD backend whose rc.conf.d override
// directory is rcConfD/rc.conf.d inside a fresh temporary directory; the
// override directory itself is not created.
func netbsdEnableBackend(t *testing.T) netbsdBackend {
	t.Helper()
	return netbsdBackend{rcConfD: filepath.Join(t.TempDir(), "rc.conf.d")}
}

// writeOverride creates b's rc.conf.d directory and writes the sshd
// override with content and mode, returning its path.
func writeOverride(t *testing.T, b netbsdBackend, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(b.rcConfD, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.rcConfD, "sshd")
	writeRcTestFile(t, path, content, mode)
	return path
}

func statOwner(t *testing.T, path string) atomicfile.Owner {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := atomicfile.OwnerOf(info)
	if !ok {
		t.Fatalf("%s carries no ownership", path)
	}
	return owner
}

// TestNetBSDSetEnabledAtomic pins the durable rc.conf.d replacement (task
// jc): the override is written to a temporary file that already holds the
// full new content and the final mode and ownership while the old override
// is still in place, then renamed over it, leaving no temporary file.
func TestNetBSDSetEnabledAtomic(t *testing.T) {
	b := netbsdEnableBackend(t)
	path := writeOverride(t, b, "sshd=NO\n", 0o640)
	owner := statOwner(t, path)

	var observed int
	restore := atomicfile.ObserveBeforeRenameForTest(func(tmp string) {
		observed++
		if filepath.Dir(tmp) != b.rcConfD || !strings.Contains(filepath.Base(tmp), ".gonftmp") {
			t.Errorf("temporary file %s is not a .gonftmp file beside %s", tmp, path)
		}
		requireRcConf(t, tmp, "sshd=YES\n", 0o640)
		if got := statOwner(t, tmp); got != owner {
			t.Errorf("temporary file owner = %+v, want %+v", got, owner)
		}
		requireRcConf(t, path, "sshd=NO\n", 0o640) // not yet replaced
	})
	defer restore()

	if err := b.setEnabled("sshd", true); err != nil {
		t.Fatalf("setEnabled: %v", err)
	}
	if observed != 1 {
		t.Fatalf("setEnabled went through atomicfile.Write %d times, want 1", observed)
	}
	requireRcConf(t, path, "sshd=YES\n", 0o640)
	requireNoRcTempFiles(t, b.rcConfD)
}

// TestNetBSDSetEnabledKeepsModeOwnerAndSymlink pins what the replaced
// override keeps: an existing file's mode and ownership (a supplementary
// group and a set-gid bit included), a symlinked override's link, and a new
// override's defaults (0644, the writer's uid and the group a new file in
// rc.conf.d gets).
func TestNetBSDSetEnabledKeepsModeOwnerAndSymlink(t *testing.T) {
	t.Run("existing file keeps mode and owner", func(t *testing.T) {
		b := netbsdEnableBackend(t)
		path := writeOverride(t, b, "sshd=YES\n", 0o600)
		owner := statOwner(t, path)
		if err := b.setEnabled("sshd", false); err != nil {
			t.Fatalf("setEnabled: %v", err)
		}
		requireRcConf(t, path, "sshd=NO\n", 0o600)
		if got := statOwner(t, path); got != owner {
			t.Errorf("override owner = %+v, want %+v", got, owner)
		}
		requireNoRcTempFiles(t, b.rcConfD)
	})
	t.Run("supplementary group and set-gid mode", func(t *testing.T) {
		gid, ok := supplementaryGroup()
		if !ok {
			t.Skip("the caller has no supplementary group")
		}
		b := netbsdEnableBackend(t)
		path := writeOverride(t, b, "sshd=NO\n", 0o640)
		if err := os.Chown(path, -1, gid); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o640|os.ModeSetgid); err != nil {
			t.Fatal(err)
		}
		if err := b.setEnabled("sshd", true); err != nil {
			t.Fatalf("setEnabled: %v", err)
		}
		want := atomicfile.Owner{UID: os.Getuid(), GID: gid}
		if got := statOwner(t, path); got != want {
			t.Errorf("override owner = %+v, want %+v", got, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSetgid == 0 {
			t.Errorf("override mode = %v, lost its set-gid bit", info.Mode())
		}
	})
	t.Run("symlink", func(t *testing.T) {
		b := netbsdEnableBackend(t)
		if err := os.MkdirAll(b.rcConfD, 0o755); err != nil {
			t.Fatal(err)
		}
		realDir := t.TempDir()
		realPath := filepath.Join(realDir, "sshd")
		writeRcTestFile(t, realPath, "sshd=NO\n", 0o640)
		link := filepath.Join(b.rcConfD, "sshd")
		if err := os.Symlink(realPath, link); err != nil {
			t.Fatal(err)
		}
		if err := b.setEnabled("sshd", true); err != nil {
			t.Fatalf("setEnabled: %v", err)
		}
		requireRcSymlink(t, link, realPath)
		requireRcConf(t, realPath, "sshd=YES\n", 0o640)
		requireNoRcTempFiles(t, realDir)
		requireNoRcTempFiles(t, b.rcConfD)
	})
	t.Run("new file 0644 owned by the writer", func(t *testing.T) {
		b := netbsdEnableBackend(t)
		if err := os.MkdirAll(b.rcConfD, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := b.setEnabled("sshd", true); err != nil {
			t.Fatalf("setEnabled: %v", err)
		}
		path := filepath.Join(b.rcConfD, "sshd")
		requireRcConf(t, path, "sshd=YES\n", 0o644)
		// A new file gets the writer's uid and the group the directory
		// hands out (the writer's on Linux, the directory's on the BSDs),
		// as a plain file created there shows.
		probe := filepath.Join(b.rcConfD, "probe")
		if err := os.WriteFile(probe, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		want := statOwner(t, probe)
		if got := statOwner(t, path); got != want {
			t.Errorf("new override owner = %+v, want %+v", got, want)
		}
	})
}

// TestNetBSDSetEnabledMissingDir pins that a missing rc.conf.d is created
// (0755) before the override is written, and that one that cannot be
// created fails naming it, writing nothing.
func TestNetBSDSetEnabledMissingDir(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		b := netbsdEnableBackend(t)
		if err := b.setEnabled("sshd", true); err != nil {
			t.Fatalf("setEnabled: %v", err)
		}
		info, err := os.Stat(b.rcConfD)
		if err != nil || !info.IsDir() {
			t.Fatalf("rc.conf.d not created as a directory (%v, %v)", info, err)
		}
		requireRcConf(t, filepath.Join(b.rcConfD, "sshd"), "sshd=YES\n", 0o644)
		requireNoRcTempFiles(t, b.rcConfD)
	})
	t.Run("cannot be created", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "etc")
		writeRcTestFile(t, parent, "not a directory", 0o644)
		b := netbsdBackend{rcConfD: filepath.Join(parent, "rc.conf.d")}
		err := b.setEnabled("sshd", true)
		if err == nil || !strings.Contains(err.Error(), b.rcConfD) {
			t.Fatalf("setEnabled err = %v, want an error naming %s", err, b.rcConfD)
		}
		requireRcConf(t, parent, "not a directory", 0o644)
	})
}

// TestNetBSDSetEnabledWriteFailureKeepsOverride pins the negative paths of
// the override replacement: a write that cannot complete (a read-only
// rc.conf.d) or an override that is a dangling symlink or not a regular
// file fails with the path named, leaving the original as it was and no
// temporary file behind.
func TestNetBSDSetEnabledWriteFailureKeepsOverride(t *testing.T) {
	t.Run("read-only directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		b := netbsdEnableBackend(t)
		path := writeOverride(t, b, "sshd=NO\n", 0o640)
		if err := os.Chmod(b.rcConfD, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(b.rcConfD, 0o700) })
		if err := b.setEnabled("sshd", true); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("setEnabled err = %v, want an error naming %s", err, path)
		}
		requireRcConf(t, path, "sshd=NO\n", 0o640)
		requireNoRcTempFiles(t, b.rcConfD)
	})
	t.Run("dangling symlink", func(t *testing.T) {
		b := netbsdEnableBackend(t)
		if err := os.MkdirAll(b.rcConfD, 0o755); err != nil {
			t.Fatal(err)
		}
		gone := filepath.Join(t.TempDir(), "gone")
		link := filepath.Join(b.rcConfD, "sshd")
		if err := os.Symlink(gone, link); err != nil {
			t.Fatal(err)
		}
		if err := b.setEnabled("sshd", true); err == nil || !strings.Contains(err.Error(), link) {
			t.Fatalf("setEnabled err = %v, want an error naming %s", err, link)
		}
		requireRcSymlink(t, link, gone)
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("dangling symlink target %s was created (%v)", gone, err)
		}
		requireNoRcTempFiles(t, b.rcConfD)
	})
	t.Run("rename fails after the temporary file was written", func(t *testing.T) {
		b := netbsdEnableBackend(t)
		path := writeOverride(t, b, "sshd=NO\n", 0o640)
		// Right before the rename, the override turns into a non-empty
		// directory, which a rename cannot replace: the write fails after
		// the temporary file was complete, and that file must be removed.
		restore := atomicfile.ObserveBeforeRenameForTest(func(string) {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
			if err := os.MkdirAll(filepath.Join(path, "keep"), 0o755); err != nil {
				t.Error(err)
			}
		})
		defer restore()
		if err := b.setEnabled("sshd", true); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("setEnabled err = %v, want an error naming %s", err, path)
		}
		if info, err := os.Stat(filepath.Join(path, "keep")); err != nil || !info.IsDir() {
			t.Errorf("directory at the override replaced (%v, %v)", info, err)
		}
		requireNoRcTempFiles(t, b.rcConfD)
	})
	t.Run("not a regular file", func(t *testing.T) {
		b := netbsdEnableBackend(t)
		if err := os.MkdirAll(b.rcConfD, 0o755); err != nil {
			t.Fatal(err)
		}
		requireFIFORefusedPromptly(t, filepath.Join(b.rcConfD, "sshd"), func() error {
			return b.setEnabled("sshd", true)
		})
		requireNoRcTempFiles(t, b.rcConfD)
	})
}
