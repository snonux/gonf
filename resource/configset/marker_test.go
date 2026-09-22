package configset

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
	"golang.org/x/sys/unix"
)

// memberOf returns the spec of member key of the fixture's set.
func (f *fixture) memberOf(key string) memberSpec {
	f.t.Helper()
	c, err := build("mail", f.options("root: paul\n"))
	if err != nil {
		f.t.Fatal(err)
	}
	for _, m := range c.spec.members {
		if m.key == key {
			return m
		}
	}
	f.t.Fatalf("no member %s", key)
	return memberSpec{}
}

func (f *fixture) markerPath(key string) string {
	m := f.memberOf(key)
	return filepath.Join(filepath.Dir(m.path), markerName("mail", m))
}

// simulateCrash leaves the state a crash right after the aliases rename
// leaves: the new aliases content is live and its marker exists.
func (f *fixture) simulateCrash(aliases string) {
	f.t.Helper()
	if err := f.sys.createMarker("mail", f.memberOf("aliases")); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.aliasesPath(), []byte(aliases), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func TestCrashBetweenRenamesFiresGatesOnce(t *testing.T) {
	f := newFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	f.simulateCrash("root: paul\npostmaster: root\n")

	resource.SetDryRun(true)
	if err := f.apply("root: paul\npostmaster: root\n"); err != nil {
		t.Fatal(err)
	}
	resource.SetDryRun(false)
	if !f.outcomeOf("aliases") {
		t.Fatal("dry-run must report the pending member")
	}
	if _, err := os.Stat(f.markerPath("aliases")); err != nil {
		t.Fatalf("dry-run must keep the marker: %v", err)
	}

	if err := f.apply("root: paul\npostmaster: root\n"); err != nil {
		t.Fatal(err)
	}
	if !f.outcomeOf("aliases") || f.outcomeOf("smtpd.conf") || !resource.AnyChanged(setID("mail")) {
		t.Fatal("the crashed member (only) must be signalled")
	}
	mustNotExist(t, f.markerPath("aliases"))
	if runs := f.validatorRuns(); len(runs) != 1 {
		t.Fatalf("signalling a pending member must not revalidate unchanged content, runs = %d", len(runs))
	}
	if err := f.apply("root: paul\npostmaster: root\n"); err != nil {
		t.Fatal(err)
	}
	if f.outcomeOf("aliases") {
		t.Fatal("a pending member must be signalled once")
	}
}

func TestMarkerIsDurableBeforeEachRename(t *testing.T) {
	f := newParallelFixture(t)
	var synced []string
	origSync, origWrite := f.sys.syncDirFD, f.sys.writeMember
	f.sys.syncDirFD = func(fd int, dir string) error {
		synced = append(synced, dir)
		return origSync(fd, dir)
	}
	f.sys.writeMember = func(target *file.Target, content []byte) error {
		for _, key := range []string{"aliases", "smtpd.conf"} {
			if _, err := os.Stat(f.markerPath(key)); err == nil && len(synced) == 0 {
				t.Errorf("marker %s exists but its directory was never synced", key)
			}
		}
		return origWrite(target, content)
	}
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	// Two creations and two removals, all in etc/mail.
	if len(synced) != 4 {
		t.Fatalf("directory syncs = %v, want one per marker creation and removal", synced)
	}
	mustNotExist(t, f.markerPath("aliases"))
	mustNotExist(t, f.markerPath("smtpd.conf"))
}

func TestMarkerExistsWhileMemberIsRenamed(t *testing.T) {
	f := newParallelFixture(t)
	orig := f.sys.writeMember
	seen := map[string]bool{}
	f.sys.writeMember = func(target *file.Target, content []byte) error {
		for _, key := range []string{"aliases", "smtpd.conf"} {
			if ok, err := f.sys.hasMarker("mail", f.memberOf(key)); err == nil && ok {
				seen[key] = true
			}
		}
		return orig(target, content)
	}
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if !seen["aliases"] || !seen["smtpd.conf"] {
		t.Fatalf("markers present during renames = %v, want each member's marker before its rename", seen)
	}
}

func TestFailedMarkerSyncPublishesNothing(t *testing.T) {
	f := newParallelFixture(t)
	f.sys.syncDirFD = func(int, string) error { return errors.New("injected fsync failure") }
	if err := f.apply("root: paul\n"); err == nil || !strings.Contains(err.Error(), "injected fsync failure") {
		t.Fatalf("apply error = %v, want the fsync failure", err)
	}
	mustNotExist(t, f.aliasesPath())
	mustNotExist(t, f.confPath())
}

func TestRollbackRemovesMarkersOnlyAfterDurableRestore(t *testing.T) {
	f := newParallelFixture(t)
	if err := os.WriteFile(f.aliasesPath(), []byte("old aliases\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Undo runs in reverse order: smtpd.conf, then aliases. When each
	// restore is made durable, that member's marker must still exist, so
	// the markers present at the two syncs are 2 and then 1.
	var present []int
	orig := f.sys.syncDirectory
	f.sys.syncDirectory = func(dir string) error {
		n := 0
		for _, key := range []string{"aliases", "smtpd.conf"} {
			if _, err := os.Stat(f.markerPath(key)); err == nil {
				n++
			}
		}
		present = append(present, n)
		return orig(dir)
	}
	failSecondWrite(f.sys, nil)
	if err := f.apply("root: paul\n"); err == nil || !strings.Contains(err.Error(), "rolled back 2 member(s)") {
		t.Fatalf("apply error = %v, want a completed rollback", err)
	}
	if len(present) != 2 || present[0] != 2 || present[1] != 1 {
		t.Fatalf("markers present at each restore sync = %v, want [2 1]", present)
	}
	mustNotExist(t, f.markerPath("aliases"))
	mustNotExist(t, f.markerPath("smtpd.conf"))
}

func TestFailedRestoreKeepsMarker(t *testing.T) {
	f := newParallelFixture(t)
	f.sys.syncDirectory = func(string) error { return errors.New("injected fsync failure") }
	failSecondWrite(f.sys, nil)
	if err := f.apply("root: paul\n"); err == nil || !strings.Contains(err.Error(), "ROLLBACK INCOMPLETE") {
		t.Fatalf("apply error = %v, want an incomplete rollback", err)
	}
	for _, key := range []string{"aliases", "smtpd.conf"} {
		if _, err := os.Stat(f.markerPath(key)); err != nil {
			t.Fatalf("a rollback that is not durable must keep the marker of %s: %v", key, err)
		}
	}
}

// A member that is pending from a crashed publication and is published again
// keeps its marker when this publication rolls it back: the restored live
// file is the crashed publication's content, which was never signalled.
func TestRollbackKeepsMarkerOfEarlierPendingMember(t *testing.T) {
	f := newParallelFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	f.simulateCrash("crashed publication\n")
	orig := f.sys.writeMember
	f.sys.writeMember = func(*file.Target, []byte) error { return errors.New("injected write failure") }
	if err := f.apply("root: paul\n"); err == nil || !strings.Contains(err.Error(), "rolled back 1 member(s)") {
		t.Fatalf("apply error = %v, want a rollback of aliases", err)
	}
	if got := readFile(t, f.aliasesPath()); got != "crashed publication\n" {
		t.Fatalf("aliases = %q, want the restored crashed content", got)
	}
	if _, err := os.Stat(f.markerPath("aliases")); err != nil {
		t.Fatalf("the earlier pending marker must survive the rollback: %v", err)
	}
	f.sys.writeMember = orig
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if !f.outcomeOf("aliases") {
		t.Fatal("the re-apply must signal aliases")
	}
	mustNotExist(t, f.markerPath("aliases"))
}

func TestSameSetNameInDifferentRecipesKeepsMarkersApart(t *testing.T) {
	t.Parallel()
	sys, outcomes := newSystem(), newOutcomeStore()
	root := t.TempDir()
	recipe := func(dir string) []opt.ConfigSetOption {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return []opt.ConfigSetOption{
			opt.ConfigFile("conf", filepath.Join(dir, "x.conf"), opt.WithContent("x\n")),
			opt.WithSetValidation("true", nil),
		}
	}
	a, b := recipe(filepath.Join(root, "a")), recipe(filepath.Join(root, "b"))
	for _, opts := range [][]opt.ConfigSetOption{a, b} {
		if err := ensure("nsd", sys, outcomes, opts); err != nil {
			t.Fatal(err)
		}
	}
	memberA := memberSpec{key: "conf", path: filepath.Join(root, "a", "x.conf")}
	if err := sys.createMarker("nsd", memberA); err != nil {
		t.Fatal(err)
	}
	if err := ensure("nsd", sys, outcomes, b); err != nil {
		t.Fatal(err)
	}
	if changed, _ := outcomes.member("nsd", "conf"); changed {
		t.Fatal("recipe b must not consume recipe a's marker")
	}
	if ok, err := sys.hasMarker("nsd", memberA); err != nil || !ok {
		t.Fatalf("recipe a's marker must survive recipe b's apply (ok=%v, err=%v)", ok, err)
	}
	if err := ensure("nsd", sys, outcomes, a); err != nil {
		t.Fatal(err)
	}
	if changed, _ := outcomes.member("nsd", "conf"); !changed {
		t.Fatal("recipe a must signal its own pending member")
	}
}

func TestMarkerNamesSeparateSetKeyAndPath(t *testing.T) {
	path := "/etc/x/y.conf"
	names := map[string]bool{
		markerName("a.b", memberSpec{key: "c", path: path}):     true,
		markerName("a", memberSpec{key: "b.c", path: path}):     true,
		markerName("a", memberSpec{key: "b", path: path}):       true,
		markerName("a", memberSpec{key: "b", path: path + "x"}): true,
	}
	if len(names) != 4 {
		t.Fatalf("marker names collide: %v", names)
	}
	for name := range names {
		if !strings.HasPrefix(name, markerPrefix) || strings.ContainsAny(name, "/\x00") {
			t.Fatalf("bad marker name %q", name)
		}
	}
}

func TestSymlinkedMarkerIsRefusedEvenToValidOwnedMarker(t *testing.T) {
	f := newParallelFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.root, "elsewhere")
	if err := os.WriteFile(target, []byte(`{"set":"mail","member":"aliases","path":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.markerPath("aliases")); err != nil {
		t.Fatal(err)
	}
	if err := f.apply("root: paul\npostmaster: root\n"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("apply with a symlinked marker: err = %v, want refusal", err)
	}
	if got := readFile(t, f.aliasesPath()); got != "root: paul\n" {
		t.Fatalf("aliases = %q: a refused marker must stop the apply before publishing", got)
	}
}

func TestMarkerOwnerIsChecked(t *testing.T) {
	f := newParallelFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	f.simulateCrash("root: paul\n")
	if ok, err := f.sys.hasMarker("mail", f.memberOf("aliases")); err != nil || !ok {
		t.Fatalf("a marker owned by the applying user must be accepted (ok=%v, err=%v)", ok, err)
	}
	orig := f.sys.euid
	f.sys.euid = func() int { return orig() + 4242 }
	if _, err := f.sys.hasMarker("mail", f.memberOf("aliases")); err == nil || !strings.Contains(err.Error(), "not owned by the applying user") {
		t.Fatalf("marker owned by another uid: err = %v, want refusal", err)
	}
}

func TestMarkersDoNotDependOnHomeOrStateEnvironment(t *testing.T) {
	t.Setenv("HOME", "/nonexistent-home")
	t.Setenv("XDG_STATE_HOME", "relative/not-used")
	f := newFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	f.simulateCrash("root: paul\n")
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if !f.outcomeOf("aliases") {
		t.Fatal("the pending member must be signalled without HOME or XDG_STATE_HOME")
	}
}

func TestLeftoverStageDetectionIsNotFooledBySetNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a+1", "a.b+2", "a-b+3", "ab+4"} {
		if err := os.Mkdir(filepath.Join(dir, stagePrefix+name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got := leftoverStages(dir, "a")
	if len(got) != 1 || filepath.Base(got[0]) != stagePrefix+"a+1" {
		t.Fatalf("leftover stages of set a = %v, want only its own", got)
	}
}

// A marker whose directory fsync fails is unlinked again: its member was
// never published, so no later apply may signal it.
func TestFailedMarkerCreationLeavesNoMarker(t *testing.T) {
	f := newParallelFixture(t)
	orig := f.sys.syncDirFD
	f.sys.syncDirFD = func(int, string) error { return errors.New("injected fsync failure") }
	if err := f.apply("root: paul\n"); err == nil {
		t.Fatal("apply must fail")
	}
	mustNotExist(t, f.markerPath("aliases"))
	f.sys.syncDirFD = orig
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	// The retry publishes and signals; no marker survives it.
	if !f.outcomeOf("aliases") {
		t.Fatal("the retry must publish aliases")
	}
	mustNotExist(t, f.markerPath("aliases"))
}

// Directory fsync is best-effort for filesystems that do not support it
// (EINVAL/ENOTSUP), like the File resource's atomic write, but a real I/O
// error fails the apply.
func TestUnsupportedDirectoryFsyncIsBestEffort(t *testing.T) {
	f := newParallelFixture(t)
	f.sys.fsyncFD = func(int) error { return unix.EINVAL }
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatalf("EINVAL from directory fsync must not fail the apply: %v", err)
	}
	if !f.outcomeOf("aliases") {
		t.Fatal("aliases must be published")
	}
	mustNotExist(t, f.markerPath("aliases"))
	f.sys.fsyncFD = func(int) error { return unix.EIO }
	if err := f.apply("root: paul\npostmaster: root\n"); err == nil || !strings.Contains(err.Error(), "input/output error") {
		t.Fatalf("EIO from directory fsync must fail the apply: %v", err)
	}
}

// A marker that cannot be removed after reporting is only a warning: the
// change is recorded and the apply succeeds. Here the unlink worked and only
// the directory fsync failed, so the marker is gone (a later apply may
// signal once more only after a power loss).
func TestMarkerRemovalFailureIsAWarning(t *testing.T) {
	f := newParallelFixture(t)
	orig := f.sys.syncDirFD
	calls := 0
	f.sys.syncDirFD = func(fd int, dir string) error {
		calls++
		if calls > 2 { // the two creations succeed, the removals fail
			return errors.New("injected fsync failure")
		}
		return orig(fd, dir)
	}
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatalf("a failed marker removal must not fail the apply: %v", err)
	}
	if !f.outcomeOf("aliases") || !f.outcomeOf("smtpd.conf") {
		t.Fatal("the published members must be reported")
	}
}

// When a member is restored durably but only its marker removal fails, the
// rollback is complete: no ROLLBACK INCOMPLETE, the stage is removed, and the
// error says the marker is removed but not durably, so a later apply may
// signal once more.
func TestRollbackWithStaleMarkerIsComplete(t *testing.T) {
	f := newParallelFixture(t)
	orig := f.sys.syncDirFD
	failing := false
	f.sys.syncDirFD = func(fd int, dir string) error {
		if failing {
			return errors.New("injected fsync failure")
		}
		return orig(fd, dir)
	}
	failSecondWrite(f.sys, func() { failing = true })
	err := f.apply("root: paul\n")
	if err == nil || strings.Contains(err.Error(), "ROLLBACK INCOMPLETE") || !strings.Contains(err.Error(), "may signal member") {
		t.Fatalf("apply error = %v, want a complete rollback that names the stale markers", err)
	}
	f.noStagingLeft()
	mustNotExist(t, f.aliasesPath())
	mustNotExist(t, f.confPath())
}

// A member whose restore itself fails keeps its marker.
func TestFailedRestoreItselfKeepsMarker(t *testing.T) {
	f := newParallelFixture(t)
	if err := os.WriteFile(f.aliasesPath(), []byte("old aliases\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.sys.linkBackup = func(int, string, string) error { return errors.New("EXDEV (injected)") }
	f.sys.restoreCopy = func(string, *copiedFile) error { return errors.New("injected restore failure") }
	failSecondWrite(f.sys, nil)
	if err := f.apply("root: paul\n"); err == nil || !strings.Contains(err.Error(), "ROLLBACK INCOMPLETE") {
		t.Fatalf("apply error = %v, want an incomplete rollback", err)
	}
	if _, err := os.Stat(f.markerPath("aliases")); err != nil {
		t.Fatalf("a member whose restore failed must keep its marker: %v", err)
	}
	// smtpd.conf did not exist before: its "restore" (removal) worked.
	mustNotExist(t, f.markerPath("smtpd.conf"))
}

func TestMarkerNameDependsOnSetName(t *testing.T) {
	m := memberSpec{key: "conf", path: "/etc/x/y.conf"}
	if markerName("x", m) == markerName("y", m) {
		t.Fatal("two sets with the same member key and path must not share a marker")
	}
}

func TestOpenLockDirsOrdersByDeviceAndInode(t *testing.T) {
	root := t.TempDir()
	var dirs []string
	for i := 0; i < 6; i++ {
		dir := filepath.Join(root, string(rune('a'+i)))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		dirs = append([]string{dir}, dirs...) // reverse creation order
	}
	held, err := openLockDirs(dirs)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, h := range held {
			_ = unix.Close(h.fd)
		}
	}()
	for i := 1; i < len(held); i++ {
		prev, cur := held[i-1], held[i]
		if prev.dev > cur.dev || (prev.dev == cur.dev && prev.ino >= cur.ino) {
			t.Fatalf("locks not in strict (dev, ino) order at %d: %+v then %+v", i, prev, cur)
		}
	}
	if len(held) != len(dirs) {
		t.Fatalf("held %d locks, want %d", len(held), len(dirs))
	}
}

// A marker whose unlink fails after reporting stays (a warning), so the next
// apply signals its member once more; this is the certain case of
// markerRemovalError, unlike the fsync-only failure above.
func TestFailedMarkerUnlinkReSignalsOnce(t *testing.T) {
	f := newParallelFixture(t)
	orig := f.sys.unlinkAt
	f.sys.unlinkAt = func(int, string, int) error { return unix.EACCES }
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatalf("a failed marker unlink must not fail the apply: %v", err)
	}
	if _, err := os.Stat(f.markerPath("aliases")); err != nil {
		t.Fatalf("the marker must stay when its unlink fails: %v", err)
	}
	f.sys.unlinkAt = orig
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if !f.outcomeOf("aliases") {
		t.Fatal("the next apply must signal the member once more")
	}
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if f.outcomeOf("aliases") {
		t.Fatal("only once more")
	}
}

func TestMarkerRemovalErrorWording(t *testing.T) {
	stays := (&markerRemovalError{key: "k", err: errors.New("x")}).Error()
	gone := (&markerRemovalError{key: "k", unlinked: true, err: errors.New("x")}).Error()
	if !strings.Contains(stays, "the marker stays") || !strings.Contains(stays, "signals member k once more") {
		t.Fatalf("unlink failure wording: %q", stays)
	}
	if !strings.Contains(gone, "not durably") || !strings.Contains(gone, "may signal member k") {
		t.Fatalf("fsync failure wording: %q", gone)
	}
}

// When a failed marker creation cannot even unlink the marker, the error says
// so instead of hiding it.
func TestFailedCreationUnlinkErrorIsReported(t *testing.T) {
	f := newParallelFixture(t)
	f.sys.syncDirFD = func(int, string) error { return errors.New("injected fsync failure") }
	f.sys.unlinkAt = func(int, string, int) error { return unix.EACCES }
	err := f.apply("root: paul\n")
	if err == nil || !strings.Contains(err.Error(), "unlink the unused marker") || !strings.Contains(err.Error(), "injected fsync failure") {
		t.Fatalf("apply error = %v, want both the fsync and the unlink failure", err)
	}
	mustNotExist(t, f.aliasesPath())
}

func TestUnsupportedDirectoryFsyncErrnosAreBestEffort(t *testing.T) {
	t.Parallel()
	sys := newSystem()
	for _, errno := range []error{unix.EINVAL, unix.ENOTSUP, unix.EOPNOTSUPP} {
		sys.fsyncFD = func(int) error { return errno }
		if err := sys.fsyncDir(-1, "/x"); err != nil {
			t.Errorf("fsyncDir with %v: %v, want best-effort nil", errno, err)
		}
	}
	sys.fsyncFD = func(int) error { return unix.EIO }
	if err := sys.fsyncDir(-1, "/x"); err == nil {
		t.Error("fsyncDir with EIO must fail")
	}
}

func TestUnsupportedFlockHasClearError(t *testing.T) {
	t.Parallel()
	sys := newSystem()
	dir := t.TempDir()
	for _, errno := range []error{unix.EBADF, unix.ENOTSUP, unix.EOPNOTSUPP} {
		sys.flock = func(int, int) error { return errno }
		_, err := sys.lockDirs([]string{dir})
		if err == nil || !strings.Contains(err.Error(), "does not support flock(2) on a directory") {
			t.Errorf("flock %v: err = %v, want the unsupported-filesystem error", errno, err)
		}
	}
	sys.flock = func(int, int) error { return unix.ENOLCK }
	_, err := sys.lockDirs([]string{dir})
	if err == nil || strings.Contains(err.Error(), "NFS") || !strings.Contains(err.Error(), "no locks available right now; retry later") {
		t.Errorf("flock ENOLCK: err = %v, want a neutral transient error", err)
	}
}
