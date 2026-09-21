package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

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
// exactly 0700 under any umask, while the pre-existing prefix is not touched.
// A umask can only narrow the 0700 that mkdir is asked for, never widen it, so
// the cases that matter are the restrictive ones: under 0277 or 0377 mkdir alone
// leaves an unusable 0500 or 0400 directory that nothing can be created in, and
// only the fchmod after creation makes it 0700 (without it this test fails, and
// the nested components could not even be created). The default 022 and the
// permissive 0 are there to show the mode is the same everywhere.
func TestSecureDirCreatesEveryComponentPrivate(t *testing.T) {
	for _, mask := range []int{0o277, 0o377, 0o077, 0o022, 0} {
		t.Run(fmt.Sprintf("umask %04o", mask), func(t *testing.T) {
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
			for _, part := range []string{dir, "world-writable", "-o <private dir>"} {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("error %q must contain %q", err, part)
				}
			}
			if got := strings.Contains(err.Error(), "chmod go-w"); got == (mode&os.ModeSticky != 0) {
				t.Fatalf("error %q: chmod advice present = %v, want it only for a non-sticky directory", err, got)
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
// nothing is created or changed through them. A symlink is reported as a
// symlink (errSymlinkComponent, whatever errno the platform gives for
// O_NOFOLLOW|O_DIRECTORY on one), a file is not mistaken for one, and the
// message names the offending component. The test looks at this package's own
// wording and error value, never at the text of an errno.
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
	for _, tc := range []struct {
		name, dir, component string
		symlink              bool
	}{
		{"file", file, "file", false},
		{"below a file", filepath.Join(file, "sub"), "file", false},
		{"symlink", link, "link", true},
		{"symlink and slash", link + "/", "link", true},
		{"symlinked parent", filepath.Join(link, "sub"), "link", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := SecureDir(tc.dir)
			if err == nil {
				t.Fatalf("SecureDir(%s) = nil, want a refusal", tc.dir)
			}
			if got := errors.Is(err, errSymlinkComponent); got != tc.symlink {
				t.Fatalf("SecureDir(%s) = %v; reported as a symlink = %v, want %v", tc.dir, err, got, tc.symlink)
			}
			if want := fmt.Sprintf("component %q: ", tc.component); !strings.Contains(err.Error(), want) {
				t.Fatalf("SecureDir(%s) = %v, want it to name %s", tc.dir, err, want)
			}
			if tc.symlink && !strings.Contains(err.Error(), "is a symlink; symlinked plan directories are refused") {
				t.Fatalf("SecureDir(%s) = %v, want the symlink refusal wording", tc.dir, err)
			}
			requireEntries(t, target)
			if got := modeOf(t, target); got != 0o700 {
				t.Fatalf("symlink target mode = %v, want 0700 untouched", got)
			}
		})
	}
}

// TestRefusalAdviceMatchesTheDirectory: a world-writable directory of the
// caller's own gets the chmod advice, a sticky one (a /tmp-style shared
// scratch directory) must NOT: telling a root run with "-o /tmp" to chmod go-w
// /tmp would break the whole system. It matters for root in particular, for
// whom the owner rule passes on /tmp, which is why the sticky case is also
// judged for a synthetic root that owns it. The wording is this package's own,
// so the asserts are exact about which advice is present and that the
// alternative (a private directory) always is.
func TestRefusalAdviceMatchesTheDirectory(t *testing.T) {
	const chmodAdvice, chooseAdvice = "chmod go-w", "-o <private dir>"
	onDisk := func(t *testing.T, mode os.FileMode) error {
		dir := filepath.Join(t.TempDir(), "shared")
		mkdirMode(t, dir, mode)
		return SecureDir(dir)
	}
	for _, tc := range []struct {
		name      string
		refuse    func(t *testing.T) error
		wantChmod bool
	}{
		{"caller-owned 0777", func(t *testing.T) error { return onDisk(t, 0o777) }, true},
		{"caller-owned 1777 (sticky)", func(t *testing.T) error { return onDisk(t, 0o777|os.ModeSticky) }, false},
		{"root-owned 1777 judged for root, like /tmp", func(*testing.T) error {
			return checkDirAttrs("/tmp", dirOf(0, 0, 0o1777), procIDs{euid: 0, egid: 0})
		}, false},
		{"root-owned 0777 judged for root", func(*testing.T) error {
			return checkDirAttrs("/srv/x", dirOf(0, 0, 0o777), procIDs{euid: 0, egid: 0})
		}, true},
		{"sticky and group-writable by a shared group", func(*testing.T) error {
			return checkDirAttrs("/srv/x", dirOf(1000, 5, 0o1770), procIDs{euid: 1000, egid: 1000})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.refuse(t)
			if err == nil {
				t.Fatal("got nil, want a refusal")
			}
			if !strings.Contains(err.Error(), chooseAdvice) {
				t.Fatalf("refusal %q must always offer %q", err, chooseAdvice)
			}
			if got := strings.Contains(err.Error(), chmodAdvice); got != tc.wantChmod {
				t.Fatalf("refusal %q: chmod advice present = %v, want %v", err, got, tc.wantChmod)
			}
		})
	}
}

// makeRivalWin makes mkdirChild behave as if another process created the
// directory first: every call first creates <parent>/<name> with mode, exactly as
// the rival would, and then asks the real mkdirat for the same name, which fails
// with EEXIST (the very race openOrCreateChild handles). The seam is restored
// when the test ends. parent is the path of the directory the descriptor of the
// call refers to.
func makeRivalWin(t *testing.T, parent string, mode os.FileMode) {
	t.Helper()
	real := mkdirChild
	t.Cleanup(func() { mkdirChild = real })
	mkdirChild = func(fd int, name string, perm uint32) error {
		rival := filepath.Join(parent, name)
		if err := os.Mkdir(rival, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(rival, mode); err != nil {
			return err
		}
		return real(fd, name, perm) // EEXIST: the name is taken now
	}
}

// TestOpenOrCreateChildLosingTheRace: when another process creates the
// directory between the failed open and our mkdir, that directory is somebody
// else's, not ours. openOrCreateChild reports created == false, so nothing
// chmods it (the rival's 0755 stays 0755; counting it as created would have
// chmod'ed it to 0700, exactly the m62 mistake for a directory we did not make),
// and the verification that follows decides. The race is made deterministic by
// makeRivalWin.
func TestOpenOrCreateChildLosingTheRace(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	mkdirMode(t, parent, 0o755)
	pfd, err := unix.Open(parent, openDirFlags, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(pfd) }()
	makeRivalWin(t, parent, 0o755)
	fd, created, err := openOrCreateChild(pfd, "x")
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	if created {
		t.Fatal("created = true after losing the creation race, want false (the directory is the rival's)")
	}
	if got := modeOf(t, filepath.Join(parent, "x")); got != 0o755 {
		t.Fatalf("raced-in directory mode = %v, want the rival's 0755 untouched", got)
	}
}

// TestSecureDirVerifiesADirectoryItLostTheRaceFor: through SecureDir, a
// directory that appears between the open and the mkdir is verified like any
// pre-existing one: an acceptable one is used and keeps its mode, an unsafe one
// (world-writable) is refused and keeps its mode too.
func TestSecureDirVerifiesADirectoryItLostTheRaceFor(t *testing.T) {
	for _, tc := range []struct {
		mode    os.FileMode
		refused bool
	}{{0o755, false}, {0o750, false}, {0o777, true}} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			mkdirMode(t, parent, 0o755)
			makeRivalWin(t, parent, tc.mode)
			err := SecureDir(filepath.Join(parent, "out"))
			if tc.refused != (err != nil) || (err != nil && !strings.Contains(err.Error(), "world-writable")) {
				t.Fatalf("SecureDir into a raced-in %v directory = %v, want refused = %v (world-writable)", tc.mode, err, tc.refused)
			}
			if got := modeOf(t, filepath.Join(parent, "out")); got != tc.mode {
				t.Fatalf("raced-in directory mode = %v, want %v untouched", got, tc.mode)
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
		_, err := NewStore(root).WriteFile("b", []byte("x"))
		requireBlobRefusal(t, err, `write blob "blobs/b": open private directory: `, filepath.Join(root, "blobs"), "world-writable")
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
			requireBlobRefusal(t, write(NewStore(root)), filepath.Join(root, "blobs"), "world-writable")
			requireEntries(t, filepath.Join(root, "blobs"))
		})
	}
}

// TestCheckExistingDirMatchesSecureDir: the FileInfo-based pre-check accepts
// and refuses exactly what SecureDir does for the same directories (own group
// and a foreign one), and words the refusal the same way (sticky bit
// included), so an up-front refusal cannot disagree with the enforcement. Every
// (group, mode) pair is its own subtest; the foreign-group ones skip, with a
// reason, where the runner has no group it can chgrp to, and the symlink case
// always runs.
func TestCheckExistingDirMatchesSecureDir(t *testing.T) {
	modes := []os.FileMode{0o700, 0o755, 0o555, 0o775, 0o757, 0o777 | os.ModeSticky, os.ModeSetgid | 0o770}
	for _, foreign := range []bool{false, true} {
		for _, mode := range modes {
			t.Run(fmt.Sprintf("foreign group %v mode %v", foreign, mode), func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "d")
				mkdirMode(t, dir, mode)
				if foreign {
					testutil.ChgrpForeign(t, dir) // skips this subtest only
				}
				info, err := os.Lstat(dir)
				if err != nil {
					t.Fatal(err)
				}
				checkErr, secureErr := CheckExistingDir(dir, info), SecureDir(dir)
				if (checkErr == nil) != (secureErr == nil) {
					t.Fatalf("CheckExistingDir = %v, SecureDir = %v; want the same verdict", checkErr, secureErr)
				}
				if checkErr != nil && checkErr.Error() != secureErr.Error() {
					t.Fatalf("messages differ:\n  check:  %v\n  secure: %v", checkErr, secureErr)
				}
			})
		}
	}
	t.Run("symlink is not a directory", func(t *testing.T) {
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
	})
}
