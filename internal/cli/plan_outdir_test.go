package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/options"
)

// The tests in this file pin m62: `gonf plan` writes into the operator's -o
// directory (the current directory by default) without changing its mode, and
// refuses one it cannot trust, before any task body runs.

// outDirWithMode creates dir with exactly mode (mkdir applies the umask) and
// the caller's own group.
func outDirWithMode(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	return testutil.MkdirMode(t, dir, mode)
}

// registerOutDirProbe registers "cli_outdir", which packages a source tree as a
// blob (so the plan needs blobs/ as well as plan.jsonl) and reports whether its
// body ran.
func registerOutDirProbe(t *testing.T) *bool {
	t.Helper()
	api.ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() { api.ResetTasks(); resource.ResetRepository() })
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	ran := new(bool)
	api.Task("cli_outdir", "", func() {
		*ran = true
		api.Dir(dst, options.WithSource(src))
	})
	return ran
}

func modePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// TestCLIPlanKeepsOutputDirMode: writing the plan into an existing -o
// directory, given explicitly or as the "." default, leaves the directory's
// mode alone (it used to become 0700, silently changing the recipe checkout
// or a shared directory); plan.jsonl is 0600 and the blobs/ directory gonf
// creates is 0700.
func TestCLIPlanKeepsOutputDirMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o750, 0o700} {
		for _, useDefault := range []bool{false, true} {
			name := mode.String() + " explicit -o"
			if useDefault {
				name = mode.String() + " default -o ."
			}
			t.Run(name, func(t *testing.T) {
				ran := registerOutDirProbe(t)
				dir := outDirWithMode(t, filepath.Join(t.TempDir(), "out"), mode)
				args := []string{"plan", "cli_outdir"}
				if useDefault {
					t.Chdir(dir)
				} else {
					args = []string{"plan", "-o", dir, "cli_outdir"}
				}
				var code int
				var stderr string
				_ = captureStdout(t, func() { code, stderr = runGonf(t, args...) })
				if code != 0 || !*ran {
					t.Fatalf("exit %d (body ran: %v), stderr: %s", code, *ran, stderr)
				}
				if got := modePerm(t, dir); got != mode {
					t.Fatalf("output directory mode = %04o, want %04o unchanged", got, mode)
				}
				if got := modePerm(t, filepath.Join(dir, "plan.jsonl")); got != 0o600 {
					t.Fatalf("plan.jsonl mode = %04o, want 0600", got)
				}
				if got := modePerm(t, filepath.Join(dir, "blobs")); got != 0o700 {
					t.Fatalf("blobs mode = %04o, want 0700", got)
				}
			})
		}
	}
}

// TestCLIPlanCreatesOutputDirPrivate: a -o directory (and its missing
// parents) that gonf creates is 0700, the mode the old code gave every -o.
func TestCLIPlanCreatesOutputDirPrivate(t *testing.T) {
	registerOutDirProbe(t)
	base := outDirWithMode(t, filepath.Join(t.TempDir(), "base"), 0o755)
	out := filepath.Join(base, "x", "y")
	var code int
	var stderr string
	_ = captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-o", out, "cli_outdir") })
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if got := modePerm(t, base); got != 0o755 {
		t.Fatalf("existing parent mode = %04o, want 0755 unchanged", got)
	}
	for _, dir := range []string{filepath.Join(base, "x"), out} {
		if got := modePerm(t, dir); got != 0o700 {
			t.Fatalf("created %s mode = %04o, want 0700", dir, got)
		}
	}
}

// unsafeOutDir is one -o directory that must be refused, with the text of the
// refusal. sticky marks a /tmp-style directory, for which the refusal must NOT
// advise chmod (its mode is not the operator's to change): only choosing another
// directory.
type unsafeOutDir struct {
	name   string
	mk     func(t *testing.T) string
	want   string
	sticky bool
}

// unsafeOutDirs are the output directories `gonf plan` refuses: world-writable
// ones (sticky /tmp-style included), whatever their group, and a group-writable
// one of a shared group. The last one needs a supplementary group to chgrp to:
// its mk skips the subtest it runs in (mk is called on the subtest's t) with the
// reason where there is none, so the case never silently drops out and never
// takes the unconditional ones with it.
func unsafeOutDirs(t *testing.T) []unsafeOutDir {
	t.Helper()
	var cases []unsafeOutDir
	for _, mode := range []os.FileMode{0o757, 0o777, 0o777 | os.ModeSticky} {
		cases = append(cases, unsafeOutDir{mode.String(), func(t *testing.T) string {
			return outDirWithMode(t, filepath.Join(t.TempDir(), "out"), mode)
		}, "world-writable", mode&os.ModeSticky != 0})
	}
	return append(cases, unsafeOutDir{"0775 shared group", func(t *testing.T) string {
		dir := outDirWithMode(t, filepath.Join(t.TempDir(), "out"), 0o700)
		testutil.ChgrpForeign(t, dir)
		if err := os.Chmod(dir, 0o775); err != nil {
			t.Fatal(err)
		}
		return dir
	}, "group-writable by group", false})
}

// TestCLIPlanRefusesUnsafeOutputDir: a -o directory that others can write
// (sticky /tmp-style ones included) or that a shared group can write, whether
// named or the "." default, is refused with an actionable message BEFORE any
// task body runs, and is left exactly as it was: nothing written, mode
// unchanged.
func TestCLIPlanRefusesUnsafeOutputDir(t *testing.T) {
	for _, tc := range unsafeOutDirs(t) {
		for _, useDefault := range []bool{false, true} {
			name := tc.name + " explicit -o"
			if useDefault {
				name = tc.name + " default -o ."
			}
			t.Run(name, func(t *testing.T) {
				ran := registerOutDirProbe(t)
				dir := tc.mk(t)
				before := testutil.Snapshot(t, dir)
				args := []string{"plan", "cli_outdir"}
				if useDefault {
					t.Chdir(dir)
				} else {
					args = []string{"plan", "-o", dir, "cli_outdir"}
				}
				code, stderr := runGonf(t, args...)
				if code != 1 {
					t.Fatalf("exit %d, stderr %q; want exit 1", code, stderr)
				}
				requireCLIRecordRefusal(t, stderr, "plan dir: ")
				for _, want := range []string{dir, tc.want, "-o <private dir>"} {
					if !strings.Contains(stderr, want) {
						t.Fatalf("stderr %q; want a refusal containing %q", stderr, want)
					}
				}
				if got := strings.Contains(stderr, "chmod go-w"); got == tc.sticky {
					t.Fatalf("stderr %q; chmod advice present = %v, want %v (never for a sticky directory)", stderr, got, !tc.sticky)
				}
				if *ran {
					t.Fatal("the task body ran; an unsafe -o must fail before any body")
				}
				testutil.RequireUnchanged(t, before, dir)
			})
		}
	}
}

// TestCLIPlanAcceptsPrivateGroupWritableOutputDir is the UPG regression: on a
// distribution with user-private groups and umask 002 (Fedora, Ubuntu, RHEL,
// Rocky) every fresh checkout is 0775 with the user's own group, and the
// default `gonf plan` (-o .) must keep working there without touching the mode.
// The whole run, directory creation included, happens under umask 002, which
// is process-wide, so this test must not run in parallel.
func TestCLIPlanAcceptsPrivateGroupWritableOutputDir(t *testing.T) {
	testutil.RequirePrivateGroupUser(t)
	for _, useDefault := range []bool{false, true} {
		name := "explicit -o"
		if useDefault {
			name = "default -o ."
		}
		t.Run(name, func(t *testing.T) {
			ran := registerOutDirProbe(t)
			dir := filepath.Join(t.TempDir(), "checkout")
			testutil.WithUmask(0o002, func() {
				if err := os.Mkdir(dir, 0o777); err != nil {
					t.Fatal(err)
				}
			})
			if err := os.Chown(dir, -1, os.Getegid()); err != nil {
				t.Fatal(err)
			}
			if got := modePerm(t, dir); got != 0o775 {
				t.Fatalf("test setup: mkdir under umask 002 made %04o, want 0775", got)
			}
			args := []string{"plan", "cli_outdir"}
			if useDefault {
				t.Chdir(dir)
			} else {
				args = []string{"plan", "-o", dir, "cli_outdir"}
			}
			var code int
			var stderr string
			testutil.WithUmask(0o002, func() {
				_ = captureStdout(t, func() { code, stderr = runGonf(t, args...) })
			})
			if code != 0 || !*ran {
				t.Fatalf("exit %d (body ran: %v), stderr: %s", code, *ran, stderr)
			}
			for path, want := range map[string]os.FileMode{dir: 0o775, filepath.Join(dir, "plan.jsonl"): 0o600, filepath.Join(dir, "blobs"): 0o700} {
				if got := modePerm(t, path); got != want {
					t.Fatalf("%s mode = %04o, want %04o", path, got, want)
				}
			}
		})
	}
}

// blobsCase is one unsafe existing blobs/ directory inside an -o directory: mk
// creates it in out and returns a second directory that must stay unchanged
// too ("" if none: what a symlink points at), and want is a substring of the
// refusal.
type blobsCase struct {
	name string
	mk   func(t *testing.T, out string) (target string)
	want string
}

// unsafeBlobsCases are the blobs/ directories `gonf plan` refuses at commit
// time: world-writable, a symlink and group-writable by a shared group. As in
// unsafeOutDirs, the shared-group case skips its own subtest (from mk, on the
// subtest's t) where the runner has no group to chgrp to.
func unsafeBlobsCases(t *testing.T) []blobsCase {
	t.Helper()
	return []blobsCase{
		{"world-writable", func(t *testing.T, out string) string {
			outDirWithMode(t, filepath.Join(out, "blobs"), 0o777)
			return ""
		}, "world-writable"},
		{"symlink", func(t *testing.T, out string) string {
			target := outDirWithMode(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
			if err := os.Symlink(target, filepath.Join(out, "blobs")); err != nil {
				t.Fatal(err)
			}
			return target
		}, `component "blobs"`},
		{"shared group", func(t *testing.T, out string) string {
			blobs := outDirWithMode(t, filepath.Join(out, "blobs"), 0o700)
			testutil.ChgrpForeign(t, blobs)
			if err := os.Chmod(blobs, 0o775); err != nil {
				t.Fatal(err)
			}
			return ""
		}, "group-writable by group"},
	}
}

// TestCLIPlanRefusesUnsafeBlobsDir: an existing blobs/ inside an acceptable -o
// directory that others can write, that a shared group can write, or that is a
// symlink is refused when the blobs are committed: after the task body ran
// (the up-front check does not look at blobs/), before anything is written, so
// plan.jsonl is not produced and the directory (and a symlink's target) stay
// exactly as they were. The message names blobs/ and reads
// "plan: RecordPlan: <reason>" with no repeated package prefix.
func TestCLIPlanRefusesUnsafeBlobsDir(t *testing.T) {
	for _, tc := range unsafeBlobsCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			ran := registerOutDirProbe(t)
			out := outDirWithMode(t, filepath.Join(t.TempDir(), "out"), 0o755)
			target := tc.mk(t, out)
			before := testutil.Snapshot(t, out)
			var targetBefore testutil.DirSnapshot
			if target != "" {
				targetBefore = testutil.Snapshot(t, target)
			}
			code, stderr := runGonf(t, "plan", "-o", out, "cli_outdir")
			if code != 1 || !*ran {
				t.Fatalf("exit %d, body ran %v, stderr %q; want exit 1 and a commit-time (post-body) refusal", code, *ran, stderr)
			}
			// One "plan: RecordPlan: " prefix, no repeated "plan:" and no "plan dir:" chain.
			requireCLIRecordRefusal(t, stderr, "blob \"blobs/")
			for _, want := range []string{filepath.Join(out, "blobs"), tc.want} {
				if !strings.Contains(stderr, want) || strings.Contains(stderr, "plan dir:") {
					t.Fatalf("stderr %q; want a refusal containing %q and no \"plan dir:\" segment", stderr, want)
				}
			}
			testutil.RequireUnchanged(t, before, out)
			if target != "" {
				testutil.RequireUnchanged(t, targetBefore, target)
			}
		})
	}
}

// TestCLIPlanRefusesForeignOutputDir: an -o directory owned by another user
// (a root-owned system directory, only ever read) is refused up front too.
func TestCLIPlanRefusesForeignOutputDir(t *testing.T) {
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
		ran := registerOutDirProbe(t)
		code, stderr := runGonf(t, "plan", "-o", dir, "cli_outdir")
		if code != 1 || !strings.Contains(stderr, dir+" is owned by uid") || *ran {
			t.Fatalf("exit %d, body ran %v, stderr %q; want an up-front \"is owned by uid\" refusal", code, *ran, stderr)
		}
		return
	}
	t.Skip("no root-owned system directory found")
}

// TestCLIWithSymlinkedTempDir is the regression for the $TMPDIR bug: with $TMPDIR
// reached through a symlink (macOS's /var/folders, where /var is a symlink) both
// `gonf plan -o out` (which stages blobs below $TMPDIR) and the local `gonf -n`
// run (which records into a gonf-plan-* directory below $TMPDIR) must work for a
// task that packages a tree blob, as they did before the blobs/ policy walked
// every component of the store's path. The -o directory is an ordinary path,
// since a symlinked one is still refused.
func TestCLIWithSymlinkedTempDir(t *testing.T) {
	ran := registerOutDirProbe(t)
	link, realTmp := testutil.SymlinkedDir(t)
	// TestMain resolved the ambient TMPDIR, so link's own symlink must be the
	// only one on the path: otherwise an ambient symlinked TMPDIR would mask
	// what this test exercises.
	parent := filepath.Dir(link)
	if resolved, err := filepath.EvalSymlinks(parent); err != nil || resolved != parent {
		t.Fatalf("parent %s of the test symlink resolves to %s (%v); want no ambient symlink", parent, resolved, err)
	}
	t.Setenv("TMPDIR", link)
	out := filepath.Join(testutil.PrivateTempDir(t), "out")
	var code int
	var stderr string
	_ = captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-o", out, "cli_outdir") })
	if code != 0 || !*ran {
		t.Fatalf("gonf plan -o: exit %d (body ran: %v), stderr: %s", code, *ran, stderr)
	}
	if got := modePerm(t, filepath.Join(out, "blobs")); got != 0o700 {
		t.Fatalf("blobs mode = %04o, want 0700", got)
	}
	*ran = false
	_ = captureStdout(t, func() { code, stderr = runGonf(t, "-n", "cli_outdir") })
	if code != 0 || !*ran {
		t.Fatalf("gonf -n: exit %d (body ran: %v), stderr: %s", code, *ran, stderr)
	}
	entries, err := os.ReadDir(realTmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp dir %s after both runs: %v, %v; want it empty", realTmp, entries, err)
	}
}

// TestCLIPlanEmptyOutDirMeansCurrentDirectory pins that `-o ”` is the current
// directory with every output-directory check, like the default "-o .": an
// unusable (here read-only) working directory is refused before any task
// body runs and gets the actionable message, instead of RecordPlan treating
// the empty path as "no plan directory" and skipping the checks.
func TestCLIPlanEmptyOutDirMeansCurrentDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses write permission checks")
	}
	ran := registerOutDirProbe(t)
	dir := outDirWithMode(t, filepath.Join(t.TempDir(), "ro"), 0o500)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Chdir(dir)
	var code int
	var stderr string
	_ = captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-o", "", "cli_outdir") })
	if code == 0 || *ran {
		t.Fatalf("exit %d (body ran: %v); want a refusal before any task body", code, *ran)
	}
	if !strings.Contains(stderr, "chmod u+w") {
		t.Fatalf("stderr = %q, want the actionable read-only refusal", stderr)
	}
}
