package configset

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
)

// lockWithin runs sys.lockDirs in a goroutine and fails the test if it
// neither returns nor errors within limit (the deadlock the (dev, ino) dedupe
// fixes).
func lockWithin(t *testing.T, sys *system, limit time.Duration, dirs ...string) (func(), error) {
	t.Helper()
	type result struct {
		unlock func()
		err    error
	}
	done := make(chan result, 1)
	go func() {
		unlock, err := sys.lockDirs(dirs)
		done <- result{unlock, err}
	}()
	select {
	case r := <-done:
		return r.unlock, r.err
	case <-time.After(limit):
		t.Fatalf("lockDirs(%v) did not return within %s", dirs, limit)
		return nil, nil
	}
}

func TestLockDirsDedupesOneDirectoryLockedTwice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	unlock, err := lockWithin(t, newSystem(), 5*time.Second, dir, dir)
	if err != nil {
		t.Fatalf("lockDirs: %v", err)
	}
	unlock()
}

func TestLockDirsRefusesSymlinkedDirectory(t *testing.T) {
	t.Parallel()
	sys := newSystem()
	root := t.TempDir()
	real := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWithin(t, sys, 5*time.Second, filepath.Join(link)); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("lockDirs through a symlink: err = %v, want refusal", err)
	}
	if _, err := lockWithin(t, sys, 5*time.Second, real, filepath.Join(link, "")); err == nil {
		t.Fatal("a symlinked spelling of a locked directory must be refused, not deadlock")
	}
}

func TestLockDirsTimesOutWithClearError(t *testing.T) {
	t.Parallel()
	sys := newSystem()
	sys.lockTimeout = 200 * time.Millisecond
	dir := t.TempDir()
	unlock, err := sys.lockDirs([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lockWithin(t, sys, 5*time.Second, dir); err == nil || !strings.Contains(err.Error(), "still holds it after") {
		t.Fatalf("contended lock: err = %v, want the bounded-wait timeout", err)
	}
}

func TestSymlinkedMemberDirectoryIsRefusedBeforeAnyWrite(t *testing.T) {
	f := newParallelFixture(t)
	link := filepath.Join(f.root, "maillink")
	if err := os.Symlink(filepath.Join(f.etc, "mail"), link); err != nil {
		t.Fatal(err)
	}
	err := f.ensure("mail",
		opt.ConfigFile("aliases", filepath.Join(link, "aliases"), opt.WithContent("x\n")),
		opt.WithSetValidation("true", nil))
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("apply through a symlinked directory: %v", err)
	}
	mustNotExist(t, f.aliasesPath())
}

func TestCopyBackupStaysOnDiskWhenRestoreFails(t *testing.T) {
	f := newParallelFixture(t)
	if err := os.WriteFile(f.aliasesPath(), []byte("old aliases\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	f.sys.linkBackup = func(int, string, string) error { return errors.New("EXDEV (injected)") }
	f.sys.restoreCopy = func(string, *copiedFile) error { return errors.New("injected restore failure") }
	failSecondWrite(f.sys, nil)

	err := f.apply("root: paul\n")
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK INCOMPLETE") {
		t.Fatalf("apply error = %v, want an incomplete rollback", err)
	}
	backups, _ := filepath.Glob(filepath.Join(f.etc, "mail", stagePrefix+"mail+*", "backups"))
	if len(backups) != 1 || !strings.Contains(err.Error(), backups[0]) {
		t.Fatalf("error must name the kept backups directory %v: %v", backups, err)
	}
	info, statErr := os.Stat(backups[0])
	if statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("backups dir mode = %v (%v), want 0700", info.Mode().Perm(), statErr)
	}
	copyPath := filepath.Join(backups[0], "0")
	if got := readFile(t, copyPath); got != "old aliases\n" {
		t.Fatalf("copy backup on disk = %q, want the pre-publication bytes", got)
	}
	if info, err := os.Lstat(copyPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o640 {
		t.Fatalf("copy backup = %v (%v), want a regular file with the original 0640 mode", info.Mode(), err)
	}
}

func TestTemplateMembersAreRefused(t *testing.T) {
	src := filepath.Join(t.TempDir(), "aliases.tmpl")
	if err := os.WriteFile(src, []byte("{{.Param}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, member := range map[string]opt.ConfigSetOption{
		"source": opt.ConfigFile("a", "/etc/mail/aliases", opt.WithSource(src)),
		"path":   opt.ConfigFile("a", "/etc/mail/aliases.tmpl", opt.WithContent("x")),
	} {
		if _, err := build("mail", []opt.ConfigSetOption{member, opt.WithSetValidation("true", nil)}); err == nil || !strings.Contains(err.Error(), ".tmpl") {
			t.Errorf("%s: .tmpl member accepted: %v", name, err)
		}
	}
}

func TestFIFOSourceIsRefused(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	_, err := build("mail", []opt.ConfigSetOption{
		opt.ConfigFile("a", "/etc/mail/aliases", opt.WithSource(fifo)), opt.WithSetValidation("true", nil),
	})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("FIFO source: err = %v, want refusal", err)
	}
}

func TestCandidatesArePrivateAndExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "cand")
	if err := writePrivate(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("candidate mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Fatalf("candidate subdirectory mode = %v, want 0700", dir.Mode().Perm())
	}
	if err := writePrivate(path, []byte("y")); err == nil {
		t.Fatal("writePrivate must refuse an existing entry (O_EXCL)")
	}
}

func TestBackupMemberRechecksEntryType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.Symlink("/etc/passwd", path); err != nil {
		t.Fatal(err)
	}
	p := &publication{set: &spec{members: []memberSpec{{key: "conf", path: path}}, sys: newSystem()}}
	if _, err := p.backupMember(0, filepath.Join(dir, "backup")); err == nil || !strings.Contains(err.Error(), "no longer a regular file") {
		t.Fatalf("backupMember on a symlink: %v", err)
	}
}

func TestWireMemberModeDefaultsToFileMode(t *testing.T) {
	m, err := memberFromWire(plan.ConfigMember{Key: "a", Path: "/etc/a"})
	if err != nil || m.mode != 0o640 {
		t.Fatalf("mode = %v (%v), want the File default 0640", m.mode, err)
	}
	m, err = memberFromWire(plan.ConfigMember{Key: "a", Path: "/etc/a", Mode: "0600", ContentB64: "eA=="})
	if err != nil || m.mode != 0o600 || string(m.content) != "x" {
		t.Fatalf("member = %+v (%v), want mode 0600 and content x", m, err)
	}
	if _, err := memberFromWire(plan.ConfigMember{Key: "a", Path: "/etc/a", Mode: "bogus"}); err == nil {
		t.Fatal("an invalid wire mode must be refused")
	}
}

func TestTargetRefusesTemplateAndLineOptions(t *testing.T) {
	for _, o := range []opt.FileOption{opt.WithTemplate, opt.WithLine("x"), opt.WithValidation("true", []string{opt.CandidatePath})} {
		if _, err := file.NewTarget("/etc/x", o); err == nil {
			t.Errorf("NewTarget accepted %T", o)
		}
	}
}
