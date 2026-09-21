package configset

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
)

// failSecondWrite replaces writeMember so the second member write performs the
// real write and then fails, like an atomic rename followed by a failed chown:
// the worst case for rollback, because the failing member IS already live.
func failSecondWrite(t *testing.T, afterFailure func()) {
	t.Helper()
	orig := writeMember
	t.Cleanup(func() { writeMember = orig })
	n := 0
	writeMember = func(target *file.Target, content []byte) error {
		n++
		if err := orig(target, content); err != nil {
			return err
		}
		if n == 2 {
			if afterFailure != nil {
				afterFailure()
			}
			return errors.New("injected chown failure")
		}
		return nil
	}
}

func TestPartialPublicationIsRolledBack(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.aliasesPath(), []byte("old aliases\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(f.aliasesPath())
	if err != nil {
		t.Fatal(err)
	}
	failSecondWrite(t, nil)

	err = f.apply("root: paul\n")
	if err == nil || !strings.Contains(err.Error(), "rolled back 2 member(s)") {
		t.Fatalf("apply error = %v, want a completed rollback", err)
	}
	// aliases existed: the exact old inode is back (hard-link backup).
	after, err := os.Stat(f.aliasesPath())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || readFile(t, f.aliasesPath()) != "old aliases\n" {
		t.Fatal("aliases was not restored to its original inode and content")
	}
	// smtpd.conf did not exist: rollback removed the published file again.
	mustNotExist(t, f.confPath())
	f.noStagingLeft()
	if _, ok := memberOutcome("mail", "aliases"); ok {
		t.Fatal("a rolled-back set must not record member outcomes")
	}

	// Replay after the failure converges forward.
	writeMember = func(target *file.Target, content []byte) error { return target.Write(content) }
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatalf("replay after rollback: %v", err)
	}
	if readFile(t, f.aliasesPath()) != "root: paul\n" || !outcomeOf(t, "aliases") {
		t.Fatal("replay must publish and report the set")
	}
}

func TestRollbackUsesCopyWhenHardLinkIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.aliasesPath(), []byte("old aliases\n"), 0o604); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.aliasesPath(), 0o604); err != nil {
		t.Fatal(err)
	}
	origLink := linkBackup
	t.Cleanup(func() { linkBackup = origLink })
	linkBackup = func(int, string, string) error { return errors.New("EXDEV (injected)") }
	failSecondWrite(t, nil)

	if err := f.apply("root: paul\n"); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("apply error = %v, want a completed rollback", err)
	}
	info, err := os.Stat(f.aliasesPath())
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, f.aliasesPath()) != "old aliases\n" || info.Mode().Perm() != 0o604 {
		t.Fatalf("copy restore: content %q mode %v, want old content and 0604", readFile(t, f.aliasesPath()), info.Mode().Perm())
	}
	mustNotExist(t, f.confPath())
}

func TestFailedRollbackKeepsBackupsAndSaysSo(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.aliasesPath(), []byte("old aliases\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Destroy the aliases backup before rollback so restoring it must fail.
	failSecondWrite(t, func() {
		matches, _ := filepath.Glob(filepath.Join(f.etc, "mail", stagePrefix+"mail+*", "backups", "0"))
		for _, m := range matches {
			_ = os.Remove(m)
		}
	})
	err := f.apply("root: paul\n")
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK INCOMPLETE") || !strings.Contains(err.Error(), "aliases") {
		t.Fatalf("apply error = %v, want an incomplete rollback naming aliases", err)
	}
	matches, _ := filepath.Glob(filepath.Join(f.etc, "mail", stagePrefix+"mail+*"))
	if len(matches) != 1 {
		t.Fatalf("staging directory must be kept after a failed rollback, found %v", matches)
	}
	// The member whose restore worked is back to its pre-apply state.
	mustNotExist(t, f.confPath())
}

func TestOverlappingPublicationsAreSerialized(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	dir := filepath.Join(t.TempDir(), "etc")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "..", "order.log")
	// Two different sets share the directory; each validator brackets a
	// sleep with begin/end lines. Serialized publications never interleave.
	script := `printf 'begin %s\n' "$1" >> "$2"; sleep 0.2; printf 'end %s\n' "$1" >> "$2"`
	set := func(name string) []opt.ConfigSetOption {
		return []opt.ConfigSetOption{
			opt.ConfigFile("conf", filepath.Join(dir, name+".conf"), opt.WithContent(name+"\n")),
			opt.WithSetValidation("sh", []string{"-c", script, "v", name, log}),
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Ensure(name, set(name)...)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "begin") || !strings.HasPrefix(lines[1], "end") ||
		strings.TrimPrefix(lines[0], "begin ") != strings.TrimPrefix(lines[1], "end ") {
		t.Fatalf("publications interleaved: %q", lines)
	}
}

func TestLockDirsBlocksUntilReleased(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockDirs([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		second, err := lockDirs([]string{dir})
		if err == nil {
			second()
		}
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("second lock acquired while the first was held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("second lock not acquired after release")
	}
}

func TestLockDirsMissingDirectoryFails(t *testing.T) {
	if _, err := lockDirs([]string{filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("locking a missing directory must fail")
	}
}
