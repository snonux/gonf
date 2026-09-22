package remote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/testutil"
)

// buildAndVerifyOwn builds with a fresh Pusher whose private build dir is
// created under root (the Pusher stands in for a separate gonf process:
// nothing in memory is shared), waits a moment so racing builders get a
// chance to interfere, checks the returned path still holds exactly the
// bytes this Pusher built, and closes the Pusher.
func buildAndVerifyOwn(root, content string, start <-chan struct{}) error {
	p := NewPusher()
	p.CrossBuildRoot = root
	defer func() { _ = p.Close() }()
	build := fakeBuild(content)
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		if start != nil {
			<-start
		}
		return build(ctx, goos, goarch, out, pkg)
	}
	path, err := p.buildGonf(context.Background(), "linux", "amd64")
	if err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	got, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(got) != content {
		return fmt.Errorf("pusher built %q but %s now holds %q", content, path, got)
	}
	return nil
}

// TestBuildGonfConcurrentPushersShipOwnBinary is the regression test for the
// shared-path race: with the old fixed $TMPDIR/gonf-cross-<os>-<arch>/gonf
// path, every Pusher building the same platform wrote to one file, so all
// but the last writer got back a path holding someone else's binary (and
// would have scp'd it), and one Pusher's cleanup deleted the others' builds.
func TestBuildGonfConcurrentPushersShipOwnBinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const n = 12
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = buildAndVerifyOwn(root, fmt.Sprintf("build-%d", i), start)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("pusher %d: %v", i, err)
		}
	}
	assertEmptyDir(t, root)
}

// Environment variables that turn TestCrossBuildHelperProcess into a helper
// for TestBuildGonfConcurrentProcessesShipOwnBinary.
const (
	crossBuildHelperRootEnv    = "GONF_TEST_CROSSBUILD_HELPER_ROOT"
	crossBuildHelperContentEnv = "GONF_TEST_CROSSBUILD_HELPER_CONTENT"
)

// TestCrossBuildHelperProcess is not a real test: it only does work when
// re-executed as a child by TestBuildGonfConcurrentProcessesShipOwnBinary.
func TestCrossBuildHelperProcess(t *testing.T) {
	root := os.Getenv(crossBuildHelperRootEnv)
	if root == "" {
		t.Skip("helper process only")
	}
	if err := buildAndVerifyOwn(root, os.Getenv(crossBuildHelperContentEnv), nil); err != nil {
		t.Fatal(err)
	}
}

// TestBuildGonfConcurrentProcessesShipOwnBinary runs the same race across
// real OS processes sharing one parent directory, the situation that made
// concurrent `go test` runs (and concurrent gonf pushes) break each other.
func TestBuildGonfConcurrentProcessesShipOwnBinary(t *testing.T) {
	t.Parallel()
	if os.Getenv(crossBuildHelperRootEnv) != "" {
		t.Skip("already a helper process")
	}
	root := t.TempDir()
	const n = 4
	outs := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrossBuildHelperProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(),
				crossBuildHelperRootEnv+"="+root,
				crossBuildHelperContentEnv+"="+fmt.Sprintf("process-%d", i))
			outs[i], errs[i] = cmd.CombinedOutput()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("helper %d: %v\n%s", i, err, outs[i])
		}
	}
	assertEmptyDir(t, root)
}

// TestBuildGonfPrivateBuildDir pins the build dir's properties: a fresh
// random gonf-cross-* dir under CrossBuildRoot, mode 0700, distinct per
// Pusher, shared by every platform one Pusher builds, and removed by Close.
func TestBuildGonfPrivateBuildDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, b := NewPusher(), NewPusher()
	for _, p := range []*Pusher{a, b} {
		p.CrossBuildRoot = root
		p.GoBuildRunner = fakeBuild("fake")
	}
	a1 := mustBuild(t, a, "linux", "amd64")
	a2 := mustBuild(t, a, "openbsd", "arm64")
	b1 := mustBuild(t, b, "linux", "amd64")

	dirA := filepath.Dir(a1)
	if filepath.Dir(a2) != dirA {
		t.Fatalf("one Pusher used two build dirs: %q, %q", a1, a2)
	}
	if filepath.Dir(b1) == dirA {
		t.Fatalf("two Pushers share build dir %q", dirA)
	}
	for _, dir := range []string{dirA, filepath.Dir(b1)} {
		if filepath.Dir(dir) != resolvedDir(t, root) || !strings.HasPrefix(filepath.Base(dir), "gonf-cross-") {
			t.Fatalf("build dir %q is not a gonf-cross-* dir under %q", dir, root)
		}
		fi, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("build dir %q mode = %v, want a 0700 directory", dir, fi.Mode())
		}
	}

	for _, p := range []*Pusher{a, b} {
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if err := p.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
	}
	assertEmptyDir(t, root)
}

// TestBuildGonfRecreatesRemovedBuildDir: if something removes the build dir
// mid-run (e.g. a temp-dir sweeper), the next build must use a fresh dir
// instead of failing or returning a stale cached path.
func TestBuildGonfRecreatesRemovedBuildDir(t *testing.T) {
	t.Parallel()
	p := newBuildTestPusher(t)
	var builds int
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		builds++
		return os.WriteFile(out, []byte("fake"), 0o755)
	}
	first := mustBuild(t, p, "linux", "amd64")
	if err := os.RemoveAll(filepath.Dir(first)); err != nil {
		t.Fatal(err)
	}
	second := mustBuild(t, p, "linux", "amd64")
	if builds != 2 {
		t.Fatalf("builds = %d, want 2 (rebuild after removal)", builds)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("rebuilt binary missing: %v", err)
	}
}

// TestCleanupBuildsRemovesDefaultPusherDir covers the CLI's cleanup entry
// point. Not parallel: it drives the package-level defaultPusher.
func TestCleanupBuildsRemovesDefaultPusherDir(t *testing.T) {
	oldBuild := defaultPusher.GoBuildRunner
	t.Cleanup(func() {
		defaultPusher.GoBuildRunner = oldBuild
		_ = CleanupBuilds()
	})
	defaultPusher.GoBuildRunner = fakeBuild("fake")

	out := mustBuild(t, defaultPusher, "gonftestcleanup", "amd64")
	if err := CleanupBuilds(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Dir(out)); !os.IsNotExist(err) {
		t.Fatalf("build dir still exists after CleanupBuilds: %v", err)
	}
	if _, ok := defaultPusher.buildCacheGet("gonftestcleanup/amd64"); ok {
		t.Fatal("CleanupBuilds left a cached path to a removed binary")
	}
}

// plantCase tampers with a finished build (binary at out) the way another
// local user or a temp sweeper could.
type plantCase struct {
	name  string
	plant func(t *testing.T, out string)
}

// plantCases: the build dir vanishes and a same-named dir or symlink holding
// an "EVIL" binary appears, the binary itself is swapped or symlinked, or
// the dir's mode is loosened.
func plantCases() []plantCase {
	evil := []byte("EVIL")
	return []plantCase{
		{"planted same-named dir", func(t *testing.T, out string) {
			mustDo(t, os.RemoveAll(filepath.Dir(out)))
			mustDo(t, os.Mkdir(filepath.Dir(out), 0o700))
			mustDo(t, os.WriteFile(out, evil, 0o755))
		}},
		{"planted symlink to foreign dir", func(t *testing.T, out string) {
			foreign := t.TempDir()
			mustDo(t, os.WriteFile(filepath.Join(foreign, filepath.Base(out)), evil, 0o755))
			mustDo(t, os.RemoveAll(filepath.Dir(out)))
			mustDo(t, os.Symlink(foreign, filepath.Dir(out)))
		}},
		{"binary swapped in place", func(t *testing.T, out string) {
			mustDo(t, os.WriteFile(out, evil, 0o755))
		}},
		{"binary replaced by symlink", func(t *testing.T, out string) {
			target := filepath.Join(t.TempDir(), "evil")
			mustDo(t, os.WriteFile(target, evil, 0o755))
			mustDo(t, os.Remove(out))
			mustDo(t, os.Symlink(target, out))
		}},
		{"dir mode loosened", func(t *testing.T, out string) {
			mustDo(t, os.Chmod(filepath.Dir(out), 0o755))
		}},
	}
}

// TestBuildGonfNeverReturnsPlantedBinary: after any plantCases tampering,
// buildGonf must not hand back the tampered path or bytes (which would be
// scp'd and installed as the remote gonf); it must rebuild in a NEW dir.
func TestBuildGonfNeverReturnsPlantedBinary(t *testing.T) {
	t.Parallel()
	for _, tc := range plantCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newBuildTestPusher(t)
			var builds atomic.Int32
			p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
				builds.Add(1)
				return os.WriteFile(out, []byte("good"), 0o755)
			}
			first := mustBuild(t, p, "linux", "amd64")
			tc.plant(t, first)

			second := mustBuild(t, p, "linux", "amd64")
			if got, err := os.ReadFile(second); err != nil || string(got) != "good" {
				t.Fatalf("buildGonf returned %s holding %q (%v), want our own build", second, got, err)
			}
			if filepath.Dir(second) == filepath.Dir(first) {
				t.Fatalf("rebuilt into the tampered dir %s, want a new private dir", filepath.Dir(first))
			}
			if n := builds.Load(); n != 2 {
				t.Fatalf("builds = %d, want 2 (tampered build must never be reused)", n)
			}
		})
	}
}

// TestBuildGonfRefusesNonStickySharedParent: in a parent that others can
// write without the sticky bit (world, or a group that is not the caller's
// private group) other users could rename our build dir away, so it is
// refused; the sticky bit, or group write for the caller's own private
// group (the umask-002 home of Fedora and most Linux distributions), is
// fine.
func TestBuildGonfRefusesNonStickySharedParent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mode    os.FileMode
		group   func(t *testing.T, dir string) // nil: the caller's egid
		wantErr bool
	}{
		{"world-writable", 0o777, nil, true},
		{"shared group 0770", 0o770, func(t *testing.T, dir string) { testutil.ChgrpForeign(t, dir) }, true},
		{"private group 0775", 0o775, func(t *testing.T, dir string) { testutil.RequirePrivateGroupUser(t) }, false},
		{"sticky world-writable", 0o777 | os.ModeSticky, nil, false},
		{"0755", 0o755, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newBuildTestPusher(t)
			mustDo(t, os.Chown(p.CrossBuildRoot, -1, os.Getegid()))
			if tc.group != nil {
				tc.group(t, p.CrossBuildRoot)
			}
			mustDo(t, os.Chmod(p.CrossBuildRoot, tc.mode))
			p.GoBuildRunner = fakeBuild("fake")
			_, err := p.buildGonf(context.Background(), "linux", "amd64")
			if tc.wantErr != (err != nil) {
				t.Fatalf("mode %v: err = %v, wantErr %v", tc.mode, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "TMPDIR") {
				t.Fatalf("error %q does not tell the user how to fix it", err)
			}
		})
	}
}

// TestBuildGonfOneBuildPerPlatformUnderConcurrency: many goroutines pushing
// to hosts of one platform through one Pusher must trigger exactly one
// build and all get the same binary (the per-key lock plus the cache).
func TestBuildGonfOneBuildPerPlatformUnderConcurrency(t *testing.T) {
	t.Parallel()
	p := newBuildTestPusher(t)
	var builds atomic.Int32
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		builds.Add(1)
		time.Sleep(20 * time.Millisecond) // widen the race window
		return os.WriteFile(out, []byte("same"), 0o755)
	}
	const n = 16
	paths := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = p.buildGonf(context.Background(), "linux", "amd64")
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || paths[i] != paths[0] {
			t.Fatalf("goroutine %d: path %q err %v, want %q", i, paths[i], errs[i], paths[0])
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("builds = %d, want exactly 1", got)
	}
	if got, err := os.ReadFile(paths[0]); err != nil || string(got) != "same" {
		t.Fatalf("binary = %q (%v)", got, err)
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustBuild(t *testing.T, p *Pusher, goos, goarch string) string {
	t.Helper()
	out, err := p.buildGonf(context.Background(), goos, goarch)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s still holds %v; every build dir must be removed", dir, entries)
	}
}
