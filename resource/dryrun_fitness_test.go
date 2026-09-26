package resource_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/snonux/gonf/api"
	opt "github.com/snonux/gonf/api/options"
	iexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/pkg"
	"github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/systemdtimer"
	"github.com/snonux/gonf/resource/timer"
)

// TestDryRunFitness is the safety net the "centralize dry-run handling"
// cleanup (t5) exists for: it applies EVERY resource/* kind with dry-run on
// and every command/exec runner stubbed to fail the test the instant a
// mutating call is attempted, and it also asserts no real filesystem
// mutation landed on disk. A resource kind that forgets its dry-run gate —
// whether it routes through the new resource.Mutate helper or still has its
// own ad-hoc "if resource.DryRun()" check — mutates for real here and this
// test catches it, regardless of which style the kind uses internally.
//
// A first version of this test drove every kind's apply() end-to-end but,
// for file and dir, only ever through the "target does not exist yet"
// branch, and, for pkg and service, only ever through one backend (dnf, and
// whichever service manager the CI host actually has). That left several
// real, independently-guarded "if resource.DryRun()" checks structurally
// unreachable: file/checksum.go's and dir/dir.go's "already matches /
// already exists, but reapply attributes" branches, and (at the time) the
// freebsd/netbsd/openbsd pkg backends and freebsd/netbsd/rcctl service
// backends, which then each carried their own guard. The
// dedicated *ReapplyAttrs subtests below now pre-create their target so that
// branch is the one exercised, and the *FreeBSD/*NetBSD/*OpenBSD/*Rcctl
// subtests force backend selection via an injected runners.PackageRunners
// Manager (pkg) and runners.ServiceRunners Manager (service) so every
// backend is driven on a
// single host regardless of its actual
// GOOS. Since x62 the dry-run guard is no longer per backend: each family
// has one guard in its shared converge.go (pkg: in applyWith; service: in
// runActions, which applyWith calls), and these subtests prove that guard holds
// for the commands every backend builds. This only proves the dry-run gate;
// it stubs the manager-detection and command-runner seams, so it cannot
// catch a bug specific to a real BSD binary's behavior that only that OS
// would exhibit.
//
// A THIRD round found 7 more structurally-unreachable "if resource.DryRun()"
// guards, all sharing the same root cause as the first two rounds: no
// subtest ever drove the specific branch the guard sits in. Fixed here:
//  1. pkg/openbsd.go's then Absent-branch guard: before x62, openbsd.go,
//     alone among the pkg backends, had TWO independent guards (its
//     p.Absent case returned early before reaching the install/upgrade
//     guard) — dryRunPkgOpenBSD only ever built a present package.
//     dryRunPkgOpenBSDAbsent answers the pkg_info probe as "installed" so
//     pkg.Absent's removal path is the one taken. Since x62 no backend has
//     a guard of its own (the only pkg guard is in resource/pkg/converge.go's
//     applyWith); the fixture still pins that the pkg_delete removal command
//     never runs in dry-run.
//  2. link/hardlink.go's two guards (create ~line 67, replace ~line 49): no
//     prior subtest ever called opt.WithHardlink at all. dryRunHardlinkCreate
//     and dryRunHardlinkReplace cover both.
//  3. dir/dir.go's ensureAbsent guard (~line 214), reachable only via
//     dir.Absent, which no prior subtest called. dryRunDirAbsent.
//  4. link/link.go's ensureAbsent guard (~line 128), reachable only via
//     link.Absent. dryRunLinkAbsent.
//  5. file/file.go's ensureAbsent guard (~line 608), reachable only via
//     file.Absent. dryRunFileAbsent.
//  6. link/symlink.go's repoint (~line 40) and replace-non-symlink (~line 55)
//     branches: dryRunLink only ever hit the "create" branch (~line 76).
//     dryRunSymlinkRepoint and dryRunSymlinkReplace cover the other two.
//  7. All 6 resource.DryRun() sites in dir/source.go (the WithSource/
//     WithSourceGlob tree-copy paths), never exercised since no subtest
//     configured a source-based sync: copySourceDir's not-exist and
//     already-exists branches (line 63, two sub-cases of the same guard),
//     copySourceSymlink's dry-run shortcut (line 174), and pruneTree/
//     pruneGlob's early-exit and per-entry would-prune branches (lines 285,
//     308, 385, 433). dryRunDirSourceFresh and dryRunDirSourceExisting
//     together cover the four copySourceDir/copySourceSymlink/pruneTree
//     sites (fresh = destination absent, hitting the not-exist and
//     early-exit halves; existing = destination pre-built, hitting the
//     already-exists and in-loop halves). dryRunDirSourceGlobFresh and
//     dryRunDirSourceGlobExisting do the same for pruneGlob's pair.
//
// Every one of these 12 new/extended subtests was independently verified by
// temporarily changing its target guard's "if resource.DryRun()" to
// "if false && resource.DryRun()", confirming ONLY that guard's subtest
// failed (siblings stayed green), and reverting before moving to the next.
//
// The pkg and service backends carry no dry-run guard at all since x62: each
// family has exactly one "if resource.DryRun()" line, in its shared
// converge.go (pkg: applyWith; service: runActions, called from applyWith),
// which
// every backend's Absent and Present paths go through. timer.go likewise
// shares one guard between its Absent and Present paths. For these, the
// existing Present-path subtests already prove that exact guard holds, so
// no separate Absent-path subtest is needed (the OpenBSD Absent fixture is
// kept to pin that pkg_delete never runs). See the call-site inventory in
// the t5 task's final `ask annotate` note for the historical call-site-to-
// subtest mapping.
//
// Each kind is its own subtest so a regression names exactly which kind (or
// which branch/backend of a kind) broke, and so kinds that require systemd
// (service/timer/daemon_reload/systemdtimer) can skip cleanly on a host
// without it instead of failing the whole suite.
func TestDryRunFitness(t *testing.T) {
	kinds := []struct {
		name string
		run  func(t *testing.T, tmp string)
	}{
		{"dir", dryRunDir},
		{"dir-reapply-attrs", dryRunDirReapplyAttrs},
		{"dir-absent", dryRunDirAbsent},
		{"dir-source-fresh", dryRunDirSourceFresh},
		{"dir-source-existing", dryRunDirSourceExisting},
		{"dir-sourceglob-fresh", dryRunDirSourceGlobFresh},
		{"dir-sourceglob-existing", dryRunDirSourceGlobExisting},
		{"file", dryRunFile},
		{"file-reapply-attrs", dryRunFileReapplyAttrs},
		{"file-absent", dryRunFileAbsent},
		{"link", dryRunLink},
		{"link-absent", dryRunLinkAbsent},
		{"symlink-repoint", dryRunSymlinkRepoint},
		{"symlink-replace", dryRunSymlinkReplace},
		{"hardlink-create", dryRunHardlinkCreate},
		{"hardlink-replace", dryRunHardlinkReplace},
		{"cmd", dryRunCmd},
		{"cron", dryRunCron},
		{"pkg", dryRunPkg},
		{"pkg-freebsd", dryRunPkgFreeBSD},
		{"pkg-netbsd", dryRunPkgNetBSD},
		{"pkg-openbsd", dryRunPkgOpenBSD},
		{"pkg-openbsd-absent", dryRunPkgOpenBSDAbsent},
		{"service", dryRunService},
		{"service-freebsd", dryRunServiceFreeBSD},
		{"service-netbsd", dryRunServiceNetBSD},
		{"service-rcctl", dryRunServiceRcctl},
		{"service-rcctl-flags", dryRunServiceRcctlFlags},
		{"daemon_reload", dryRunDaemonReload},
		{"timer", dryRunTimer},
		{"systemdtimer", dryRunSystemdTimer},
		{"user", dryRunUser},
	}

	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			resource.SetDryRun(true)
			k.run(t, t.TempDir())
		})
	}
}

// dryRunUser exercises the public User → PlanDraft → user plan.Handler path.
// The random name makes an existing account extraordinarily unlikely, so all
// platform probes reach the mutation decisions; resource.Mutate must then
// suppress group/user creation while still reporting the pending change.
func dryRunUser(t *testing.T, _ string) {
	name := fmt.Sprintf("gonf-dryrun-user-%d", os.Getpid())
	api.User(name,
		opt.WithPrimaryGroup(name),
		opt.WithSupplementaryGroups("wheel"),
	)
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	if !resource.AnyChanged("Group["+name+"]", "User["+name+"]") {
		t.Fatal("dry-run user resource did not report suppressed mutations")
	}
}

// requireSystemd skips t when systemd unit management is not available on
// this host (mirrors the skip convention already used by
// resource/timer/timer_test.go).
func requireSystemd(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd resources are Linux-only")
	}
	if !systemd.Detected() {
		t.Skip("systemd not detected on this host")
	}
}

// fakeSystemctlRunner answers systemctl is-active/is-enabled queries with
// "inactive"/"disabled" (exit 1, no error — a normal negative per
// resource/systemd's client.go) so the caller's convergence logic decides a
// mutation is needed, then flags *mutated if anything else (enable, start,
// restart, disable, stop, daemon-reload, ...) is actually invoked. Under a
// correct dry-run gate that second call must never happen.
func fakeSystemctlRunner(mutated *bool) func(name string, args ...string) (string, string, int, error) {
	return func(name string, args ...string) (string, string, int, error) {
		for _, a := range args {
			if a == "is-active" || a == "is-enabled" {
				return "", "", 1, nil
			}
		}
		*mutated = true
		return "", "", 0, nil
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not touch %s (Lstat err=%v)", path, err)
	}
}

func dryRunDir(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "newdir")
	dir.Present(path)
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, path)
}

// dryRunDirReapplyAttrs exercises the OTHER branch of dir.go's
// ensureDirectorySelf: dryRunDir above always targets a brand-new directory,
// so it only ever reaches the os.IsNotExist branch. A directory that already
// exists takes the err == nil branch instead, which notes StatusOK and then
// falls all the way through to the unconditional applyAttributesTo
// chmod/chown at the bottom of the function -- guarded only by its own
// early "if resource.DryRun() { return nil }". This fixture pre-creates the
// directory with a mode that does not match what Dir.Present will apply, so
// a real chmod under a broken guard is observable as a mode change.
func dryRunDirReapplyAttrs(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "existingdir")
	const existingMode = os.FileMode(0o700)
	if err := os.Mkdir(path, existingMode); err != nil {
		t.Fatal(err)
	}
	// Pin the mode explicitly: os.Mkdir's mode argument is subject to the
	// process umask, so a stray umask could otherwise make existingMode not
	// actually land on disk.
	if err := os.Chmod(path, existingMode); err != nil {
		t.Fatal(err)
	}
	dir.Present(path, opt.WithMode(0o750))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != existingMode {
		t.Fatalf("dry-run must not chmod %s (directory already existed): got mode %v, want unchanged %v", path, got, existingMode)
	}
}

// dryRunDirAbsent exercises dir.go's ensureAbsent guard (~line 214): neither
// dryRunDir nor dryRunDirReapplyAttrs above ever calls dir.Absent, so
// ensureAbsent's own "if resource.DryRun()" check (distinct from
// ensureDirectorySelf's) was never reached. This fixture pre-creates a real
// directory and targets it with dir.Absent, so a broken guard is observable
// as a real os.Remove.
func dryRunDirAbsent(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "existingdir-absent")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	dir.Absent(path)
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("dry-run must not remove %s (Lstat err=%v)", path, err)
	}
}

// dryRunDirSourceFresh exercises three of dir/source.go's six independent
// "if resource.DryRun()" guards in one pass, all against a destination tree
// that does not exist yet (ensureDirectorySelf's own dry-run guard skips
// creating d.path, mirroring dryRunDir): copySourceDir's "target does not
// exist" branch (source.go:63, the os.IsNotExist sub-case), copySourceSymlink's
// dry-run shortcut (source.go:174, which delegates to noteSourceSymlinkDryRun
// instead of link.Ensure so a not-yet-materialized destination sibling is
// previewed instead of refused), and pruneTree's early "destination does not
// exist yet, nothing to walk" exit (source.go:285). dryRunDirSourceExisting
// below exercises the other half of the first and third of these.
func dryRunDirSourceFresh(t *testing.T, tmp string) {
	src := filepath.Join(tmp, "src-fresh")
	if err := os.MkdirAll(filepath.Join(src, "subdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "subdir", "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "linktarget.txt"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("linktarget.txt", filepath.Join(src, "rel-link")); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "dest-fresh")
	dir.Present(dest, opt.WithSource(src), opt.WithPrune)
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}

	assertAbsent(t, dest)
	assertAbsent(t, filepath.Join(dest, "subdir"))
	assertAbsent(t, filepath.Join(dest, "subdir", "file.txt"))
	assertAbsent(t, filepath.Join(dest, "linktarget.txt"))
	assertAbsent(t, filepath.Join(dest, "rel-link"))
}

// dryRunDirSourceExisting exercises the OTHER halves of copySourceDir's and
// pruneTree's guards exercised above: the destination tree is pre-created in
// full, so copySourceDir's "already exists" branch (source.go:74-89, whose
// non-dry-run twin unconditionally chmods/chowns via applyAttributesTo) and
// pruneTree's per-entry would-prune branch (source.go:308, guarding a real
// os.RemoveAll) are the ones exercised instead.
func dryRunDirSourceExisting(t *testing.T, tmp string) {
	src := filepath.Join(tmp, "src-existing")
	if err := os.MkdirAll(filepath.Join(src, "subdir"), 0o750); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "dest-existing")
	const destMode = os.FileMode(0o700)
	if err := os.MkdirAll(dest, destMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, destMode); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dest, "subdir")
	if err := os.Mkdir(subdir, destMode); err != nil {
		t.Fatal(err)
	}
	// Pin the mode explicitly (umask): see dryRunDirReapplyAttrs.
	if err := os.Chmod(subdir, destMode); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dest, "stale.txt")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir.Present(dest, opt.WithSource(src), opt.WithPrune, opt.WithMode(0o750))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != destMode {
		t.Fatalf("dry-run must not chmod existing source-tree subdirectory %s: got mode %v, want unchanged %v", subdir, got, destMode)
	}
	if _, err := os.Lstat(stale); err != nil {
		t.Fatalf("dry-run must not prune %s: %v", stale, err)
	}
}

// dryRunDirSourceGlobFresh exercises pruneGlob's early "destination does not
// exist yet" exit (source.go:385), the WithSourceGlob twin of
// dryRunDirSourceFresh's pruneTree coverage above.
func dryRunDirSourceGlobFresh(t *testing.T, tmp string) {
	src := filepath.Join(tmp, "srcglob-fresh")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "match.txt"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "destglob-fresh")
	dir.Present(dest, opt.WithSourceGlob(filepath.Join(src, "*.txt")), opt.WithPrune)
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}

	assertAbsent(t, dest)
	assertAbsent(t, filepath.Join(dest, "match.txt"))
}

// dryRunDirSourceGlobExisting exercises pruneGlob's per-entry would-prune
// branch (source.go:433), guarding a real os.Remove of a stale destination
// file whose basename does not match the configured glob's keep-set.
func dryRunDirSourceGlobExisting(t *testing.T, tmp string) {
	src := filepath.Join(tmp, "srcglob-existing")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "match.txt"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "destglob-existing")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dest, "stale.txt")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir.Present(dest, opt.WithSourceGlob(filepath.Join(src, "*.txt")), opt.WithPrune)
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(stale); err != nil {
		t.Fatalf("dry-run must not prune %s: %v", stale, err)
	}
}

func dryRunFile(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "newfile.txt")
	file.Present(path, opt.WithContent("hello"))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, path)
}

// dryRunFileReapplyAttrs exercises the OTHER branch of file/checksum.go's
// ensureFile: dryRunFile above always targets a brand-new file, so it only
// ever reaches the "changed" branch that routes through resource.Mutate.
// This fixture pre-creates a file whose content already matches what
// File.Present will apply, but whose mode does not, so ensureFile's
// "!changed" branch is the one under test: it notes StatusOK and then, for
// anything that is not gated by dry-run, falls through to the unconditional
// f.applyAttributesTo chmod/chown -- guarded only by its own "if
// resource.DryRun() { return nil }" ahead of that call. A real chmod under a
// broken guard is observable as a mode change.
func dryRunFileReapplyAttrs(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "existing.txt")
	const content = "hello"
	const existingMode = os.FileMode(0o644)
	if err := os.WriteFile(path, []byte(content), existingMode); err != nil {
		t.Fatal(err)
	}
	// Pin the mode explicitly: os.WriteFile's mode argument is subject to the
	// process umask, so a stray umask could otherwise make existingMode not
	// actually land on disk.
	if err := os.Chmod(path, existingMode); err != nil {
		t.Fatal(err)
	}
	file.Present(path, opt.WithContent(content), opt.WithMode(0o600))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != existingMode {
		t.Fatalf("dry-run must not chmod %s (content already matched): got mode %v, want unchanged %v", path, got, existingMode)
	}
}

// dryRunFileAbsent exercises file.go's ensureAbsent guard (~line 608): the
// only path that reaches it is file.Absent, which neither dryRunFile nor
// dryRunFileReapplyAttrs above ever calls. This fixture pre-creates a real
// file and targets it with file.Absent, so a broken guard is observable as a
// real os.Remove.
func dryRunFileAbsent(t *testing.T, tmp string) {
	path := filepath.Join(tmp, "existing-file-absent.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	file.Absent(path)
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("dry-run must not remove %s (Lstat err=%v)", path, err)
	}
}

func dryRunLink(t *testing.T, tmp string) {
	target := filepath.Join(tmp, "target")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "link")
	link.Present(path, opt.WithSymlink(target))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, path)
}

// dryRunLinkAbsent exercises link.go's ensureAbsent guard (~line 128): the
// only path that reaches it is link.Absent, which dryRunLink above never
// calls (it builds a fresh symlink via link.Present). This fixture
// pre-creates a real symlink and targets it with link.Absent, so a broken
// guard is observable as a real os.Remove.
func dryRunLinkAbsent(t *testing.T, tmp string) {
	target := filepath.Join(tmp, "absent-target")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "existing-link-absent")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	link.Absent(path)
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("dry-run must not remove %s (Lstat err=%v)", path, err)
	}
}

// dryRunSymlinkRepoint exercises symlink.go's "repoint an existing symlink"
// guard (~line 40): dryRunLink above always targets a path with nothing at
// it yet, so it only ever reaches the "create" branch at the bottom of
// ensureSymlink. This fixture pre-creates a symlink pointing at one target
// and reconfigures it to point at a different (also real, so the target-exists
// assert passes) one, so a broken guard is observable as a real os.Remove
// followed by a real os.Symlink.
func dryRunSymlinkRepoint(t *testing.T, tmp string) {
	oldTarget := filepath.Join(tmp, "repoint-old-target")
	if err := os.WriteFile(oldTarget, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	newTarget := filepath.Join(tmp, "repoint-new-target")
	if err := os.WriteFile(newTarget, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "repoint-link")
	if err := os.Symlink(oldTarget, path); err != nil {
		t.Fatal(err)
	}
	link.Present(path, opt.WithSymlink(newTarget))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("dry-run must not remove symlink %s: %v", path, err)
	}
	if got != oldTarget {
		t.Fatalf("dry-run must not repoint %s: got target %q, want unchanged %q", path, got, oldTarget)
	}
}

// dryRunSymlinkReplace exercises symlink.go's "replace an existing non-symlink
// entry" guard (~line 55): a plain file (not a symlink) sits at path, so
// ensureSymlink's err==nil-but-not-a-symlink branch is the one exercised
// instead of either the repoint branch above or the create branch dryRunLink
// covers. A broken guard is observable as the regular file being moved aside
// (replaceWithLink's path.old) and replaced by a real symlink.
func dryRunSymlinkReplace(t *testing.T, tmp string) {
	target := filepath.Join(tmp, "replace-target")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "replace-existing")
	const original = "not-a-symlink"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	link.Present(path, opt.WithSymlink(target))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("dry-run must not remove %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("dry-run must not convert %s into a symlink", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("dry-run must not replace %s content: got %q, want %q", path, got, original)
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create aside backup %s.old", path)
	}
}

// dryRunHardlinkCreate exercises hardlink.go's "create a new hardlink" guard
// (~line 67): no prior subtest ever calls opt.WithHardlink at all (dryRunLink
// only ever builds a symlink), so neither of hardlink.go's two independently
// guarded branches were reachable before this and dryRunHardlinkReplace below.
func dryRunHardlinkCreate(t *testing.T, tmp string) {
	target := filepath.Join(tmp, "hardlink-target")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "hardlink-new")
	link.Present(path, opt.WithHardlink(target))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, path)
}

// dryRunHardlinkReplace exercises hardlink.go's "replace an existing entry"
// guard (~line 49): a plain file (not already hardlinked to target) sits at
// path, so ensureHardlink's "info, err := os.Lstat(l.path); err == nil,
// !sameInode" branch is the one exercised instead of the create branch above.
// A broken guard is observable as the regular file being moved aside
// (replaceWithLink's path.old) and replaced by a real hardlink to target.
func dryRunHardlinkReplace(t *testing.T, tmp string) {
	target := filepath.Join(tmp, "hardlink-replace-target")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "hardlink-replace-existing")
	const original = "original-content"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	link.Present(path, opt.WithHardlink(target))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("dry-run must not remove %s: %v", path, err)
	}
	if os.SameFile(info, targetInfo) {
		t.Fatalf("dry-run must not actually hardlink %s to %s", path, target)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("dry-run must not replace %s content: got %q, want %q", path, got, original)
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create aside backup %s.old", path)
	}
}

func dryRunCmd(t *testing.T, tmp string) {
	var mutated bool
	rs := &runners.Set{Command: &runners.CommandRunners{
		Run: func(opts iexec.Opts, name string, args ...string) (string, string, int, error) {
			mutated = true
			return "", "", 0, nil
		},
		Probe: func(name string, args ...string) (string, string, int, error) {
			// Guard probes (Unless/OnlyIf) are not used by this fixture, but
			// keep the seam wired so a future probe call is caught too.
			mutated = true
			return "", "", 0, nil
		},
	}}
	cmd.Present("true", nil)
	// api.ApplyWithRunners was unexported (task 3f2, an accidental public
	// test seam whose sole real parameter type — internal/runners.Set — an
	// external module could never name anyway); this cross-package test
	// injects the fake command runner through the proper module-internal
	// equivalent instead, same shape and coverage.
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not execute the command")
	}
}

func dryRunCron(t *testing.T, tmp string) {
	var mutated bool
	cr := &runners.CronRunners{
		Read: func(name string, args ...string) (string, string, int, error) {
			// crontab -l on an empty crontab: exit 0, empty stdout.
			return "", "", 0, nil
		},
		Write: func(stdin, name string, args ...string) (string, string, int, error) {
			mutated = true
			return "", "", 0, nil
		},
	}
	cron.Present("fit-job", opt.WithCommand("true"))
	if err := testapply.ApplyWithRunners(&runners.Set{Cron: cr}); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not write the crontab")
	}
}

func dryRunPkg(t *testing.T, tmp string) {
	// Force a deterministic backend regardless of the host OS/distro so this
	// subtest is portable.
	var mutated bool
	rs := &runners.Set{Package: &runners.PackageRunners{
		Manager: func() (string, error) { return "dnf", nil },
		Run: func(name string, args ...string) (string, string, int, error) {
			if name == "rpm" {
				// rpm -q is a read-only probe: report "not installed" so the
				// backend decides a package install is needed.
				return "", "not installed", 1, nil
			}
			mutated = true
			return "", "", 0, nil
		},
	}}
	pkg.Present("fit-pkg")
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run the package manager")
	}
}

// dryRunPkgBackend forces resource/pkg's package-manager detection to mgr
// (via an injected runners.PackageRunners Manager) and runs the same
// fixture as dryRunPkg against it, flagging a mutation the moment the
// backend issues a command isProbe does not recognize as its own read-only
// "is it installed" check. dryRunPkg above only ever forces "dnf"; these
// subtests route the freebsd/netbsd/openbsd backends through the single
// "if resource.DryRun()" guard in resource/pkg's applyWith (converge.go),
// proving no backend's command slips past it.
func dryRunPkgBackend(t *testing.T, mgr string, isProbe func(name string, args []string) bool) {
	t.Helper()
	var mutated bool
	rs := &runners.Set{Package: &runners.PackageRunners{
		Manager: func() (string, error) { return mgr, nil },
		Run: func(name string, args ...string) (string, string, int, error) {
			if isProbe(name, args) {
				// Not-installed probe response, so the backend decides an
				// install is needed and (absent its dry-run guard) would
				// issue a real mutating command next.
				return "", "not installed", 1, nil
			}
			mutated = true
			return "", "", 0, nil
		},
	}}
	pkg.Present("fit-pkg")
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatalf("dry-run must not run the package manager (%s backend)", mgr)
	}
}

func dryRunPkgFreeBSD(t *testing.T, tmp string) {
	dryRunPkgBackend(t, "freebsd", func(name string, args []string) bool {
		// freebsd.go probes with "pkg info -e NAME"; every other "pkg ..."
		// call (install/upgrade/remove) is a mutation.
		return name == "pkg" && len(args) > 0 && args[0] == "info"
	})
}

func dryRunPkgNetBSD(t *testing.T, tmp string) {
	dryRunPkgBackend(t, "netbsd", func(name string, args []string) bool {
		// netbsd.go probes via the pkg_info binary (netbsdPkgInfo, unexported
		// so its literal is duplicated here) and mutates via the separate
		// pkgin binary (netbsdPkgin) -- distinct binaries, so the probe is
		// identified by name alone.
		return name == "/usr/sbin/pkg_info"
	})
}

func dryRunPkgOpenBSD(t *testing.T, tmp string) {
	dryRunPkgBackend(t, "openbsd", func(name string, args []string) bool {
		// openbsd.go probes via pkg_info and mutates via pkg_add/pkg_delete
		// -- distinct binaries, so the probe is identified by name alone.
		return name == "pkg_info"
	})
}

// dryRunPkgOpenBSDAbsent exercises the OpenBSD removal path: dryRunPkgOpenBSD
// above only ever calls pkg.Present (the pkg_add install command). Since x62
// every backend shares one dry-run guard in resource/pkg's applyWith, but
// the removal command (pkg_delete, a different binary) is still chosen by a
// separate branch, so it keeps its own fixture. This one answers the
// pkg_info probe as "installed" so pkg.Absent's removal path decides a real
// pkg_delete is needed, and asserts it never runs.
func dryRunPkgOpenBSDAbsent(t *testing.T, tmp string) {
	var mutated bool
	rs := &runners.Set{Package: &runners.PackageRunners{
		Manager: func() (string, error) { return "openbsd", nil },
		Run: func(name string, args ...string) (string, string, int, error) {
			if name == "pkg_info" {
				// installed: exit 0.
				return "", "", 0, nil
			}
			mutated = true
			return "", "", 0, nil
		},
	}}
	pkg.Absent("fit-pkg")
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run pkg_delete for the openbsd backend")
	}
}

func dryRunService(t *testing.T, tmp string) {
	requireSystemd(t)
	var mutated bool
	rs := &runners.Set{Systemd: &runners.SystemdRunners{Run: fakeSystemctlRunner(&mutated)}}
	service.Present("fit-service")
	// testapply.ApplyWithRunners injects the fake systemctl runner directly
	// (task 4e2), replacing the process-global internal/testseam fake this
	// used before; see dryRunCmd's own comment for why this cross-package
	// test cannot reach api's unexported applyWithRunners.
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run systemctl for the service")
	}
}

// dryRunServiceBackend forces resource/service's service-manager detection
// to mgr (via an injected runners.ServiceRunners Manager) and probes the service as
// enabled-but-stopped, so a correctly-gated apply would queue exactly one
// "start" action and a broken guard would actually run it. classify
// inspects a runner call's args and reports "running" or "enabled" for a
// probe (answered not-running / enabled respectively) or "" for anything
// else, which flags a mutation. The freebsd/netbsd/rcctl backends share the
// single "if resource.DryRun()" guard in resource/service's runActions
// (converge.go, called from applyWith); the injected Manager lets
// these subtests drive each backend's actions through that guard on a
// single Linux CI host instead of only on the backend's own OS.
func dryRunServiceBackend(t *testing.T, mgr string, classify func(args []string) string) {
	t.Helper()
	var mutated bool
	fake := func(name string, args ...string) (string, string, int, error) {
		switch classify(args) {
		case "running":
			return "", "", 1, nil // not running
		case "enabled":
			return "", "", 0, nil // enabled
		default:
			mutated = true
			return "", "", 0, nil
		}
	}
	rs := &runners.Set{Service: &runners.ServiceRunners{
		Run:     fake,
		Manager: func() (string, error) { return mgr, nil },
	}}
	service.Present("fit-service")
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatalf("dry-run must not run the service manager (%s backend)", mgr)
	}
}

func dryRunServiceFreeBSD(t *testing.T, tmp string) {
	dryRunServiceBackend(t, "freebsd", func(args []string) string {
		// freebsd.go probes with "service NAME status"/"service NAME enabled".
		if len(args) == 2 && args[1] == "status" {
			return "running"
		}
		if len(args) == 2 && args[1] == "enabled" {
			return "enabled"
		}
		return ""
	})
}

func dryRunServiceNetBSD(t *testing.T, tmp string) {
	dryRunServiceBackend(t, "netbsd", func(args []string) string {
		// netbsd.go probes with "service NAME onestatus" and "service -e NAME".
		// enabled=true here deliberately avoids ever exercising the
		// enable/disable action, which writes to netbsdRcConfD (default
		// /etc/rc.conf.d) directly instead of through this runner seam.
		if len(args) == 2 && args[1] == "onestatus" {
			return "running"
		}
		if len(args) == 2 && args[0] == "-e" {
			return "enabled"
		}
		return ""
	})
}

func dryRunServiceRcctl(t *testing.T, tmp string) {
	dryRunServiceBackend(t, "rcctl", func(args []string) string {
		// rcctl.go probes with "rcctl check NAME" and "rcctl get NAME status".
		if len(args) >= 2 && args[0] == "check" {
			return "running"
		}
		if len(args) >= 2 && args[0] == "get" {
			return "enabled"
		}
		return ""
	})
}

// dryRunServiceRcctlFlags pins that a WithFlags change is only reported in
// a dry run: rcctl get NAME flags probes, rcctl set never runs, and the
// service is noted would-change.
func dryRunServiceRcctlFlags(t *testing.T, _ string) {
	var mutated bool
	fake := func(name string, args ...string) (string, string, int, error) {
		switch {
		case args[0] == "check", args[0] == "get" && args[2] == "status":
			return "", "", 0, nil // running and enabled
		case args[0] == "get" && args[2] == "flags":
			return "-old\n", "", 0, nil
		}
		mutated = true
		return "", "", 0, nil
	}
	rs := &runners.Set{Service: &runners.ServiceRunners{
		Run:     fake,
		Manager: func() (string, error) { return "rcctl", nil },
	}}
	service.Present("fit-service", opt.WithFlags("-new"), opt.WithRestart)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run rcctl set or restart")
	}
	if !resource.AnyChanged("Service[fit-service]") {
		t.Fatal("dry-run flags change was not reported")
	}
}

func dryRunDaemonReload(t *testing.T, tmp string) {
	requireSystemd(t)
	var mutated bool
	rs := &runners.Set{Systemd: &runners.SystemdRunners{Run: fakeSystemctlRunner(&mutated)}}
	systemd.Present()
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run systemctl daemon-reload")
	}
}

func dryRunTimer(t *testing.T, tmp string) {
	requireSystemd(t)
	var mutated bool
	rs := &runners.Set{Systemd: &runners.SystemdRunners{Run: fakeSystemctlRunner(&mutated)}}
	timer.Present("fit-timer")
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run systemctl for the timer")
	}
}

func dryRunSystemdTimer(t *testing.T, tmp string) {
	requireSystemd(t)
	// WithUser + a HOME override keeps this fixture inside the temp dir even
	// if the dry-run gate has a bug: the unit dir is derived from
	// os.UserHomeDir(), never a hardcoded /etc path, when WithUser is set.
	t.Setenv("HOME", tmp)
	var mutated bool
	rs := &runners.Set{Systemd: &runners.SystemdRunners{Run: fakeSystemctlRunner(&mutated)}}
	systemdtimer.Present("fit-systemdtimer",
		opt.WithUser,
		opt.WithCommand("/bin/true"),
		opt.WithOnCalendar("*-*-* *:00:00"),
	)
	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not run systemctl for the systemd timer")
	}
	unitDir := filepath.Join(tmp, ".config", "systemd", "user")
	assertAbsent(t, unitDir)
	assertAbsent(t, filepath.Join(unitDir, "fit-systemdtimer.service"))
	assertAbsent(t, filepath.Join(unitDir, "fit-systemdtimer.timer"))
}
