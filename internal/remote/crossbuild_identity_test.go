package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/snonux/gonf/internal/dirperm"
)

// These tests reach the build-DIRECTORY identity checks (verifyBuildDir,
// checkDirIdentity, isOurDir, removeBuildDir, checkBuildParent) directly.
// The plantCases in crossbuild_test.go are all caught by the binary SHA-256
// check first, so without these tests the directory checks could be removed
// unnoticed.

// resolvedDir returns dir with symlinks resolved (macOS: /var -> /private/var),
// matching the resolved parent newBuildDir creates build dirs under.
func resolvedDir(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// dirSwap replaces a build dir the way another user (after a temp sweeper
// removed ours) could, and reports what must never be touched afterwards.
type dirSwap struct {
	name string
	// swap replaces dir and returns a path whose contents (a single
	// "marker" file) gonf must leave alone.
	swap func(t *testing.T, dir string) (untouched string)
}

// dirSwaps: a same-named real directory (created BEFORE ours is removed, so
// it cannot reuse our inode number; only os.SameFile tells it apart, as it
// is ours by uid and mode 0700 in a test), and a symlink to a foreign dir.
func dirSwaps() []dirSwap {
	return []dirSwap{
		{"planted same-named dir", func(t *testing.T, dir string) string {
			planted, err := os.MkdirTemp(filepath.Dir(dir), "planted-*")
			mustDo(t, err)
			mustDo(t, os.WriteFile(filepath.Join(planted, "marker"), []byte("x"), 0o600))
			mustDo(t, os.RemoveAll(dir))
			mustDo(t, os.Rename(planted, dir))
			return dir
		}},
		{"symlink to foreign dir", func(t *testing.T, dir string) string {
			foreign := t.TempDir()
			mustDo(t, os.WriteFile(filepath.Join(foreign, "marker"), []byte("x"), 0o600))
			mustDo(t, os.RemoveAll(dir))
			mustDo(t, os.Symlink(foreign, dir))
			return foreign
		}},
	}
}

// assertOnlyMarker fails unless dir still holds exactly its marker file.
func assertOnlyMarker(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%s: %v (planted/foreign dir must be left alone)", dir, err)
	}
	if len(entries) != 1 || entries[0].Name() != "marker" {
		t.Fatalf("%s holds %v, want only the untouched marker", dir, entries)
	}
}

// TestBuildGonfOtherPlatformAfterDirSwap: after the build dir is swapped, a
// build for a DIFFERENT platform (no cached binary, so no SHA check) must
// not reuse the swapped path: privateBuildDir's re-check has to create a
// fresh private dir. Close must then leave the planted dir / the symlink's
// target untouched while removing our own new dir.
func TestBuildGonfOtherPlatformAfterDirSwap(t *testing.T) {
	t.Parallel()
	for _, sw := range dirSwaps() {
		t.Run(sw.name, func(t *testing.T) {
			t.Parallel()
			p := newBuildTestPusher(t)
			p.GoBuildRunner = fakeBuild("good")
			swapped := filepath.Dir(mustBuild(t, p, "linux", "amd64"))
			untouched := sw.swap(t, swapped)

			second := mustBuild(t, p, "openbsd", "arm64")
			if filepath.Dir(second) == swapped || strings.HasPrefix(second, untouched+string(filepath.Separator)) {
				t.Fatalf("built into the swapped dir: %s", second)
			}
			assertOnlyMarker(t, untouched)

			mustDo(t, p.Close())
			assertOnlyMarker(t, untouched)
			if _, err := os.Lstat(filepath.Dir(second)); !os.IsNotExist(err) {
				t.Fatalf("our new build dir survived Close: %v", err)
			}
		})
	}
}

// TestCloseLeavesSwappedCurrentDirAlone: the CURRENT build dir is swapped
// and Close runs with no build in between, so only removeBuildDir's
// identity guard stands between Close and deleting someone else's dir (or
// the symlink standing in for ours).
func TestCloseLeavesSwappedCurrentDirAlone(t *testing.T) {
	t.Parallel()
	for _, sw := range dirSwaps() {
		t.Run(sw.name, func(t *testing.T) {
			t.Parallel()
			p := newBuildTestPusher(t)
			p.GoBuildRunner = fakeBuild("good")
			swapped := filepath.Dir(mustBuild(t, p, "linux", "amd64"))
			untouched := sw.swap(t, swapped)

			mustDo(t, p.Close())
			assertOnlyMarker(t, untouched)
			if _, err := os.Lstat(swapped); err != nil {
				t.Fatalf("Close removed the swapped-in %s: %v", swapped, err)
			}
		})
	}
}

// midBuildSwaps replace the build dir while the build runs, each leaving a
// regular file we own at the output path (so the binary checks pass) and
// returning the path of that file, which gonf must not delete: it is not
// in our dir any more.
func midBuildSwaps() map[string]func(t *testing.T, out string) string {
	return map[string]func(t *testing.T, out string) string{
		"planted same-named dir": func(t *testing.T, out string) string {
			dir := filepath.Dir(out)
			planted, err := os.MkdirTemp(filepath.Dir(dir), "planted-*")
			mustDo(t, err)
			mustDo(t, os.WriteFile(filepath.Join(planted, filepath.Base(out)), []byte("foreign"), 0o755))
			mustDo(t, os.RemoveAll(dir))
			mustDo(t, os.Rename(planted, dir))
			return out
		},
		"symlink to foreign dir": func(t *testing.T, out string) string {
			foreign := t.TempDir()
			kept := filepath.Join(foreign, filepath.Base(out))
			mustDo(t, os.WriteFile(kept, []byte("foreign"), 0o755))
			mustDo(t, os.RemoveAll(filepath.Dir(out)))
			mustDo(t, os.Symlink(foreign, filepath.Dir(out)))
			return kept
		},
	}
}

// TestBuildGonfRejectsDirSwappedDuringBuild: a dir swapped while the build
// runs must be caught by buildInto's post-build dir re-check: no path is
// returned or cached, and the foreign file at the output path (reached
// through the planted dir or the symlink) is left alone.
func TestBuildGonfRejectsDirSwappedDuringBuild(t *testing.T) {
	t.Parallel()
	for name, swap := range midBuildSwaps() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newBuildTestPusher(t)
			var builds int
			var kept string
			p.GoBuildRunner = func(_ context.Context, _, _, out, _ string) error {
				builds++
				mustDo(t, os.WriteFile(out, []byte("good"), 0o755))
				if builds == 1 {
					kept = swap(t, out)
				}
				return nil
			}
			if path, err := p.buildGonf(context.Background(), "linux", "amd64"); err == nil {
				t.Fatalf("buildGonf returned %s from a dir swapped during the build", path)
			}
			if got, err := os.ReadFile(kept); err != nil || string(got) != "foreign" {
				t.Fatalf("foreign file %s = %q (%v), want it untouched", kept, got, err)
			}
			second := mustBuild(t, p, "linux", "amd64")
			if builds != 2 {
				t.Fatalf("builds = %d, want 2 (the swapped build must not be cached)", builds)
			}
			if got, err := os.ReadFile(second); err != nil || string(got) != "good" {
				t.Fatalf("rebuilt binary = %q (%v)", got, err)
			}
		})
	}
}

// TestCheckDirIdentityRejectsNonDirWithMatchingInode simulates inode-number
// reuse: the recorded identity matches the object now at the path, but it
// is a symlink, not our directory. Only the real-directory check catches it.
func TestCheckDirIdentityRejectsNonDirWithMatchingInode(t *testing.T) {
	t.Parallel()
	link := filepath.Join(t.TempDir(), "link")
	mustDo(t, os.Symlink(t.TempDir(), link))
	info, err := os.Lstat(link)
	mustDo(t, err)
	if _, err := checkDirIdentity(&buildDirState{path: link, info: info}); err == nil {
		t.Fatal("a symlink was accepted as our build dir")
	}
}

// TestCheckDirIdentityRejectsForeignOwner simulates a replacement owned by
// another user: chown needs root, so our own dir is checked against a
// different uid instead.
func TestCheckDirIdentityRejectsForeignOwner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	info, err := os.Lstat(dir)
	mustDo(t, err)
	d := &buildDirState{path: dir, info: info}
	if _, err := checkDirIdentityAs(d, os.Geteuid()); err != nil {
		t.Fatalf("own dir refused: %v", err)
	}
	if _, err := checkDirIdentityAs(d, os.Geteuid()+1); err == nil || !strings.Contains(err.Error(), "not owned by") {
		t.Fatalf("dir owned by another uid accepted: %v", err)
	}
}

// TestLoosenedModeDirIsRetiredButStillRemoved: our own dir whose only fault
// is a loosened mode must not be built into again, but it is still ours by
// inode and uid, so Close removes it (checked here directly, not left to
// t.TempDir cleanup) and its fatal hook stays until then.
func TestLoosenedModeDirIsRetiredButStillRemoved(t *testing.T) {
	p := newBuildTestPusher(t)
	p.GoBuildRunner = fakeBuild("good")
	loosened := filepath.Dir(mustBuild(t, p, "linux", "amd64"))
	mustDo(t, os.Chmod(loosened, 0o755))

	second := mustBuild(t, p, "openbsd", "arm64")
	if filepath.Dir(second) == loosened {
		t.Fatalf("built into the loosened dir %s", loosened)
	}
	mustDo(t, p.Close())
	for _, dir := range []string{loosened, filepath.Dir(second)} {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s survived Close: %v", dir, err)
		}
	}
}

// TestBuildParentChecks: the parent is resolved through symlinks and the
// RESOLVED path is checked, including every ancestor.
func TestBuildParentChecks(t *testing.T) {
	t.Parallel()
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp-link")
	mustDo(t, os.Symlink(realDir, link))

	p := newBuildTestPusher(t)
	p.CrossBuildRoot = link
	p.GoBuildRunner = fakeBuild("good")
	out := mustBuild(t, p, "linux", "amd64")
	if got, want := filepath.Dir(filepath.Dir(out)), resolvedDir(t, realDir); got != want {
		t.Fatalf("build dir parent = %s, want the resolved %s", got, want)
	}

	shared := t.TempDir()
	mustDo(t, os.Chmod(shared, 0o777))
	sharedLink := filepath.Join(t.TempDir(), "shared-link")
	mustDo(t, os.Symlink(shared, sharedLink))
	if _, err := checkBuildParent(sharedLink); err == nil {
		t.Fatal("symlink to a non-sticky world-writable dir was accepted")
	}

	// An ancestor, not just the direct parent, that is world-writable
	// without the sticky bit must be refused too. checkBuildParent names the
	// resolved ancestor, so compare against resolvedDir (they differ when
	// the temp dir is reached through a symlink, e.g. macOS /var).
	openAncestor := t.TempDir()
	mustDo(t, os.Chmod(openAncestor, 0o777))
	private := filepath.Join(openAncestor, "private")
	mustDo(t, os.Mkdir(private, 0o700))
	if _, err := checkBuildParent(private); err == nil || !strings.Contains(err.Error(), resolvedDir(t, openAncestor)) {
		t.Fatalf("private dir under a non-sticky world-writable ancestor accepted: %v", err)
	}
}

// fakeInfo is a FileInfo with a chosen mode, owner and group, so the parent
// rules can be exercised for owners and groups a test cannot chown to.
type fakeInfo struct {
	os.FileInfo
	mode os.FileMode
	st   *syscall.Stat_t
}

func (f fakeInfo) Mode() os.FileMode { return f.mode }
func (f fakeInfo) IsDir() bool       { return f.mode.IsDir() }
func (f fakeInfo) Sys() any          { return f.st }

// fakeDirInfo returns a fakeInfo based on a real directory's Lstat.
func fakeDirInfo(t *testing.T, mode os.FileMode, uid, gid uint32) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(t.TempDir())
	mustDo(t, err)
	st := *fi.Sys().(*syscall.Stat_t)
	st.Uid, st.Gid = uid, gid
	return fakeInfo{FileInfo: fi, mode: mode, st: &st}
}

// TestCheckBuildParentInfoRules pins the per-directory rule with synthetic
// ids (uid 1000 with private group 1000), independent of who runs the test.
func TestCheckBuildParentInfoRules(t *testing.T) {
	t.Parallel()
	me := dirperm.IDs{EUID: 1000, EGID: 1000}
	dir := os.ModeDir
	for _, tc := range []struct {
		name     string
		mode     os.FileMode
		uid, gid uint32
		want     string // "" = accepted, else a substring of the refusal
	}{
		{"own 0700", dir | 0o700, 1000, 1000, ""},
		{"own 0755", dir | 0o755, 1000, 100, ""},
		{"root-owned sticky /tmp", dir | os.ModeSticky | 0o777, 0, 0, ""},
		{"root-owned 0755", dir | 0o755, 0, 0, ""},
		{"own 0775 private group", dir | 0o775, 1000, 1000, ""},
		{"root-owned 0775 private group", dir | 0o775, 0, 1000, ""},
		{"own 0775 shared group", dir | 0o775, 1000, 100, "not your private group"},
		{"shared group with sticky", dir | os.ModeSticky | 0o770, 1000, 100, ""},
		{"own 0777", dir | 0o777, 1000, 1000, "world-writable"},
		{"foreign owner", dir | 0o755, 2000, 2000, "not owned by uid 1000 or root"},
		{"unmapped nobody", dir | 0o755, 65534, 65534, "not owned by"},
		{"symlink", os.ModeSymlink | 0o777, 1000, 1000, "is a symlink"},
		{"file", 0o644, 1000, 1000, "not a directory"},
	} {
		err := checkBuildParentInfo("/x", fakeDirInfo(t, tc.mode, tc.uid, tc.gid), me)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	// Root never gets the private-group exception (gid 0 is "wheel").
	root := dirperm.IDs{}
	if err := checkBuildParentInfo("/x", fakeDirInfo(t, dir|0o775, 0, 0), root); err == nil {
		t.Error("root accepted a group-writable gid-0 parent")
	}
}

// TestCheckBuildParentRefusesForeignOwnedAncestor: ownership is checked on
// every ancestor, not just the direct parent. The lstat seam reports one
// ancestor as owned by another user (chown needs root).
func TestCheckBuildParentRefusesForeignOwnedAncestor(t *testing.T) {
	t.Parallel()
	leaf := filepath.Join(resolvedDir(t, t.TempDir()), "a", "b")
	mustDo(t, os.MkdirAll(leaf, 0o700))
	foreign := filepath.Dir(leaf) // ".../a"
	lstat := func(path string) (os.FileInfo, error) {
		fi, err := os.Lstat(path)
		if err != nil || path != foreign {
			return fi, err
		}
		st := *fi.Sys().(*syscall.Stat_t)
		st.Uid = uint32(os.Geteuid() + 1)
		return fakeInfo{FileInfo: fi, mode: fi.Mode(), st: &st}, nil
	}
	if os.Geteuid()+1 == 0 {
		t.Skip("uid overflow")
	}
	_, err := checkBuildParentAs(leaf, dirperm.Current(), lstat)
	if err == nil || !strings.Contains(err.Error(), foreign+" is not owned by") {
		t.Fatalf("foreign-owned ancestor %s accepted: %v", foreign, err)
	}
	if _, err := checkBuildParentAs(leaf, dirperm.Current(), os.Lstat); err != nil {
		t.Fatalf("the same path with its real owners refused: %v", err)
	}
}

// TestCheckBuildParentChecksTheRootDir: the walk ends at "/" and checks it
// too, as the doc promises; a world-writable non-sticky "/" (faked through
// the lstat seam) is refused.
func TestCheckBuildParentChecksTheRootDir(t *testing.T) {
	t.Parallel()
	leaf := resolvedDir(t, t.TempDir())
	lstat := func(path string) (os.FileInfo, error) {
		fi, err := os.Lstat(path)
		if err != nil || path != "/" {
			return fi, err
		}
		st := *fi.Sys().(*syscall.Stat_t)
		return fakeInfo{FileInfo: fi, mode: os.ModeDir | 0o777, st: &st}, nil
	}
	_, err := checkBuildParentAs(leaf, dirperm.Current(), lstat)
	if err == nil || !strings.Contains(err.Error(), "/ (mode 0777)") {
		t.Fatalf("world-writable non-sticky / accepted or misreported: %v", err)
	}
}

// TestCheckBuildParentRelativeUnderSymlinkedCwd: a relative parent (e.g.
// TMPDIR=tmp) while the working directory is reached through a symlink
// ($PWD containing it) must be made absolute BEFORE resolving symlinks, so
// the checked and returned path has no symlink in it. Not parallel: it
// changes the working directory and $PWD.
func TestCheckBuildParentRelativeUnderSymlinkedCwd(t *testing.T) {
	realDir := resolvedDir(t, t.TempDir())
	mustDo(t, os.Mkdir(filepath.Join(realDir, "sub"), 0o700))
	link := filepath.Join(t.TempDir(), "cwd-link")
	mustDo(t, os.Symlink(realDir, link))
	t.Chdir(link)
	t.Setenv("PWD", link)

	got, err := checkBuildParent("sub")
	if err != nil {
		t.Fatalf("relative parent under a symlinked cwd refused: %v", err)
	}
	if want := filepath.Join(realDir, "sub"); got != want {
		t.Fatalf("resolved parent = %s, want %s", got, want)
	}
}

// TestBuildParentAcceptsRootOwnedStickyTmp: a root-owned sticky /tmp is the
// normal parent of every build dir, so it must be accepted for a non-root
// user; dropping the "or root" rule would break every push.
func TestBuildParentAcceptsRootOwnedStickyTmp(t *testing.T) {
	t.Parallel()
	fi, err := os.Lstat("/tmp")
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSticky == 0 {
		t.Skip("/tmp is not a sticky directory here")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		t.Skip("/tmp is not root-owned here")
	}
	ids := dirperm.Current()
	if ids.EUID == 0 {
		ids = dirperm.IDs{EUID: 12345, EGID: 12345} // check as a non-root user even when run as root
	}
	if err := checkBuildParentInfo("/tmp", fi, ids); err != nil {
		t.Fatalf("root-owned sticky /tmp refused for uid %d: %v", ids.EUID, err)
	}
}

// TestNewBuildDirRefusesNonPrivateResult: a restrictive umask (here one
// removing the owner's execute bit) makes MkdirTemp produce a dir that is
// not exactly 0700; newBuildDir must refuse it and remove it. Not parallel:
// the umask is process-wide.
func TestNewBuildDirRefusesNonPrivateResult(t *testing.T) {
	root := t.TempDir()
	old := syscall.Umask(0o177)
	d, err := newBuildDir(root)
	syscall.Umask(old)
	if err == nil {
		_ = os.RemoveAll(d.path)
		t.Fatal("a build dir that is not mode 0700 was accepted")
	}
	assertEmptyDir(t, root)
}
