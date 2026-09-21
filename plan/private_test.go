package plan

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// mkdirMode creates dir with exactly mode (os.Mkdir is subject to the umask,
// so it chmods afterwards) and the caller's own group, for the directories the
// tests pre-create.
func mkdirMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	testutil.MkdirMode(t, dir, mode)
}

// modeOf is the permission, setuid/setgid and sticky bits of path (no
// symlink following), the whole "mode" a test compares before and after.
func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode() &^ os.ModeDir
}

// requireEntries fails unless dir contains exactly the given entry names.
func requireEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries of %s = %v, want %v", dir, got, want)
	}
}

// TestSecureDirLeavesExistingDirAlone is the m62 regression: a directory that
// already existed and that we own is verified, never rewritten, whatever its
// (safe) mode is. SecureDir used to chmod it to 0700, which silently changed
// the recipe checkout behind `gonf plan`'s default "-o ." and broke the other
// readers of shared output directories.
func TestSecureDirLeavesExistingDirAlone(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o750, 0o700, 0o555, 0o500, os.ModeSetgid | 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "existing")
			mkdirMode(t, dir, mode)
			want := modeOf(t, dir)
			if err := SecureDir(dir); err != nil {
				t.Fatalf("SecureDir(%s, mode %v) = %v, want success", dir, mode, err)
			}
			if got := modeOf(t, dir); got != want {
				t.Fatalf("mode after SecureDir = %v, want it unchanged (%v)", got, want)
			}
			requireEntries(t, dir)
		})
	}
}

// TestSecureDirCurrentDirectory covers the "-o ." default: the final component
// is the process's working directory, which must be verified (and here
// accepted) without changing its mode.
func TestSecureDirCurrentDirectory(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "checkout")
	mkdirMode(t, cwd, 0o755)
	t.Chdir(cwd)
	if err := SecureDir("."); err != nil {
		t.Fatalf("SecureDir(.) = %v, want success", err)
	}
	if got := modeOf(t, cwd); got != 0o755 {
		t.Fatalf("cwd mode = %v, want 0755 unchanged", got)
	}
	if err := os.Chmod(cwd, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := SecureDir("."); err == nil || !strings.Contains(err.Error(), "world-writable") {
		t.Fatalf("SecureDir(.) on a world-writable cwd = %v, want a refusal", err)
	}
	if got := modeOf(t, cwd); got != 0o777 {
		t.Fatalf("cwd mode = %v, want the refused directory left at 0777", got)
	}
}

// TestSecureDirCreatesEveryComponentPrivate: components SecureDir creates are
// 0700 whatever the umask says (a permissive one must not widen them, a
// restrictive one must not leave an unusable 0500), while the pre-existing
// prefix is not touched.
func TestSecureDirCreatesEveryComponentPrivate(t *testing.T) {
	for name, mask := range map[string]int{"permissive umask 0": 0, "default umask 022": 0o022, "restrictive umask 0277": 0o277} {
		t.Run(name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "base")
			mkdirMode(t, base, 0o755)
			testutil.WithUmask(mask, func() {
				if err := SecureDir(filepath.Join(base, "a", "b", "c")); err != nil {
					t.Fatal(err)
				}
			})
			if got := modeOf(t, base); got != 0o755 {
				t.Fatalf("pre-existing prefix mode = %v, want 0755 unchanged", got)
			}
			for _, part := range []string{"a", "a/b", "a/b/c"} {
				if got := modeOf(t, filepath.Join(base, filepath.FromSlash(part))); got != 0o700 {
					t.Fatalf("created %s mode = %v, want 0700", part, got)
				}
			}
		})
	}
}

// TestSecureDirAcceptsItsOwnDirectoryAgain: a directory this package created
// on an earlier run (0700, ours) passes the verification of the next run.
func TestSecureDirAcceptsItsOwnDirectoryAgain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	for range 2 {
		if err := SecureDir(dir); err != nil {
			t.Fatal(err)
		}
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Fatalf("mode = %v, want 0700", got)
	}
}

// TestSecureDirRefusesWorldWritableDirs: a world-writable directory is refused
// whatever its group (a user-private group user's own group included) and
// whether or not it is sticky (sticky stops deleting foreign entries, but not
// planting entries, and plan material must not depend on that). The error names
// the directory and the reason, the directory is left exactly as it was, and
// nothing is created inside.
func TestSecureDirRefusesWorldWritableDirs(t *testing.T) {
	for _, mode := range []os.FileMode{0o702, 0o707, 0o757, 0o777, 0o777 | os.ModeSticky, os.ModeSetgid | 0o777} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "shared")
			mkdirMode(t, dir, mode)
			want := modeOf(t, dir)
			err := SecureDir(dir)
			if err == nil {
				t.Fatalf("SecureDir(%s, mode %v) = nil, want a refusal", dir, mode)
			}
			for _, part := range []string{dir, "world-writable", "chmod go-w", "-o <private dir>"} {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("error %q must contain %q", err, part)
				}
			}
			if got := modeOf(t, dir); got != want {
				t.Fatalf("mode after refusal = %v, want it untouched (%v)", got, want)
			}
			requireEntries(t, dir)
		})
	}
}

// TestSecureDirRefusesForeignOwnedDir: a directory owned by another user is
// refused even when it is not writable by anyone else, since its owner may
// rename or replace what we put in it. The example is a root-owned system
// directory (never modified: SecureDir only reads it), so the test needs no
// privilege.
func TestSecureDirRefusesForeignOwnedDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("nothing is foreign to root here")
	}
	for _, dir := range []string{"/usr", "/etc", "/opt"} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) == os.Geteuid() {
			continue
		}
		err = SecureDir(dir)
		if err == nil || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "owned by uid") {
			t.Fatalf("SecureDir(%s) = %v, want an \"owned by uid\" refusal naming it", dir, err)
		}
		return
	}
	t.Skip("no root-owned system directory found")
}

// TestSecureDirRefusesNonDirectories: a file, a symlink to a directory and a
// symlinked ancestor are refused (every component is opened O_NOFOLLOW), and
// nothing is created or changed through them.
func TestSecureDirRefusesNonDirectories(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	mkdirMode(t, target, 0o700)
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"file":              file,
		"below a file":      filepath.Join(file, "sub"),
		"symlink":           link,
		"symlink and slash": link + "/",
		"symlinked parent":  filepath.Join(link, "sub"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := SecureDir(dir); err == nil {
				t.Fatalf("SecureDir(%s) = nil, want a refusal", dir)
			}
			requireEntries(t, target)
			if got := modeOf(t, target); got != 0o700 {
				t.Fatalf("symlink target mode = %v, want 0700 untouched", got)
			}
		})
	}
}

// TestWritePrivateFileRefusesWritableDir: the file writer shares the directory
// policy, so secret plan material never lands in a world-writable directory
// (the group rule has its own tests); a plain 0755 directory of ours is fine
// and keeps its mode, while the file itself is owner-only.
func TestWritePrivateFileRefusesWritableDir(t *testing.T) {
	root := t.TempDir()
	unsafe := filepath.Join(root, "unsafe")
	mkdirMode(t, unsafe, 0o777)
	if err := WritePrivateFile(unsafe, "plan.jsonl", []byte("secret")); err == nil {
		t.Fatal("WritePrivateFile into a world-writable directory = nil, want a refusal")
	}
	requireEntries(t, unsafe)

	shared := filepath.Join(root, "shared")
	mkdirMode(t, shared, 0o755)
	if err := WritePrivateFile(shared, "plan.jsonl", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, shared); got != 0o755 {
		t.Fatalf("directory mode = %v, want 0755 unchanged", got)
	}
	assertOwnerOnlyFile(t, filepath.Join(shared, "plan.jsonl"))
}

// TestStoreWriteFileBlobsDirPolicy: Store.WriteFile creates blobs/ 0700 in a
// plan directory it does not otherwise touch, keeps an existing blobs/ of
// ours as it is (0755 included), and refuses one that others can write (the
// group rule is pinned in private_group_test.go), without writing a blob into
// it.
func TestStoreWriteFileBlobsDirPolicy(t *testing.T) {
	t.Run("creates blobs private and leaves the plan dir alone", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "plan")
		mkdirMode(t, root, 0o755)
		testutil.WithUmask(0, func() {
			if _, err := NewStore(root).WriteFile("b", []byte("x")); err != nil {
				t.Fatal(err)
			}
		})
		if got := modeOf(t, root); got != 0o755 {
			t.Fatalf("plan dir mode = %v, want 0755 unchanged", got)
		}
		assertOwnerOnlyDir(t, filepath.Join(root, "blobs"))
	})
	t.Run("keeps an existing blobs dir", func(t *testing.T) {
		root := t.TempDir()
		mkdirMode(t, filepath.Join(root, "blobs"), 0o755)
		if _, err := NewStore(root).WriteFile("b", []byte("x")); err != nil {
			t.Fatal(err)
		}
		if got := modeOf(t, filepath.Join(root, "blobs")); got != 0o755 {
			t.Fatalf("blobs mode = %v, want 0755 unchanged", got)
		}
	})
	t.Run("refuses a writable blobs dir", func(t *testing.T) {
		root := t.TempDir()
		mkdirMode(t, filepath.Join(root, "blobs"), 0o777)
		if _, err := NewStore(root).WriteFile("b", []byte("x")); err == nil {
			t.Fatal("WriteFile into a world-writable blobs dir = nil, want a refusal")
		}
		requireEntries(t, filepath.Join(root, "blobs"))
		if got := modeOf(t, filepath.Join(root, "blobs")); got != 0o777 {
			t.Fatalf("blobs mode = %v, want 0777 untouched", got)
		}
	})
}

// TestStoreTreeBlobsDirPolicy: tree and glob blobs go through the same blobs/
// policy as single-file blobs: created 0700, an existing 0755 one kept, a
// world-writable one refused before anything is cleared or written.
func TestStoreTreeBlobsDirPolicy(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	writers := map[string]func(*Store) error{
		"tree": func(s *Store) error { _, err := s.WriteTree("t", src); return err },
		"glob": func(s *Store) error { _, err := s.WriteGlob("g", filepath.Join(src, "*")); return err },
	}
	for name, write := range writers {
		t.Run(name+" creates blobs private", func(t *testing.T) {
			root := t.TempDir()
			testutil.WithUmask(0, func() {
				if err := write(NewStore(root)); err != nil {
					t.Fatal(err)
				}
			})
			assertOwnerOnlyDir(t, filepath.Join(root, "blobs"))
		})
		t.Run(name+" keeps an existing blobs dir", func(t *testing.T) {
			root := t.TempDir()
			mkdirMode(t, filepath.Join(root, "blobs"), 0o755)
			if err := write(NewStore(root)); err != nil {
				t.Fatal(err)
			}
			if got := modeOf(t, filepath.Join(root, "blobs")); got != 0o755 {
				t.Fatalf("blobs mode = %v, want 0755 unchanged", got)
			}
		})
		t.Run(name+" refuses a writable blobs dir", func(t *testing.T) {
			root := t.TempDir()
			mkdirMode(t, filepath.Join(root, "blobs"), 0o777)
			err := write(NewStore(root))
			if err == nil || !strings.Contains(err.Error(), "world-writable") {
				t.Fatalf("write into a world-writable blobs dir = %v, want a refusal", err)
			}
			requireEntries(t, filepath.Join(root, "blobs"))
		})
	}
}

// TestCheckExistingDirMatchesSecureDir: the FileInfo-based pre-check accepts
// and refuses exactly what SecureDir does for the same directories (own group
// and, where the caller has a supplementary group, a foreign one), and words the
// refusal the same way (sticky bit included), so an up-front refusal cannot
// disagree with the enforcement.
func TestCheckExistingDirMatchesSecureDir(t *testing.T) {
	modes := []os.FileMode{0o700, 0o755, 0o555, 0o775, 0o757, 0o777 | os.ModeSticky, os.ModeSetgid | 0o770}
	_, haveForeign := testutil.FindForeignGroup()
	for _, foreign := range []bool{false, true} {
		if foreign && !haveForeign {
			continue
		}
		for _, mode := range modes {
			dir := filepath.Join(t.TempDir(), "d")
			mkdirMode(t, dir, mode)
			if foreign {
				testutil.ChgrpForeign(t, dir)
			}
			info, err := os.Lstat(dir)
			if err != nil {
				t.Fatal(err)
			}
			checkErr, secureErr := CheckExistingDir(dir, info), SecureDir(dir)
			if (checkErr == nil) != (secureErr == nil) {
				t.Fatalf("mode %v foreign group %v: CheckExistingDir = %v, SecureDir = %v; want the same verdict", mode, foreign, checkErr, secureErr)
			}
			if checkErr != nil && checkErr.Error() != secureErr.Error() {
				t.Fatalf("mode %v foreign group %v: messages differ:\n  check:  %v\n  secure: %v", mode, foreign, checkErr, secureErr)
			}
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckExistingDir(link, info); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("CheckExistingDir(symlink) = %v, want a refusal", err)
	}
}
