package file

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	. "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestGetChecksum(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	checksum := getChecksum(path)
	if checksum == [32]byte{} {
		t.Error("expected non-zero checksum")
	}

	zeroChecksum := getChecksum(filepath.Join(dir, "no-such-file"))
	var expectedZero [32]byte
	if zeroChecksum != expectedZero {
		t.Errorf("expected zero checksum for missing file, got %x", zeroChecksum)
	}
}

func TestAtomicWriteCreatesFileWithContentAndMode(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")
	content := []byte("atomic content")

	if err := atomicWrite(path, content, 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("expected %q, got %q", content, got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("expected mode 0o644, got %v", info.Mode().Perm())
	}
	// No temporary file may be left behind.
	assertNoLeftoverTempFiles(t, dir)
}

func TestAtomicWriteOverwritesExistingFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := atomicWrite(path, []byte("new content"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading target file: %v", err)
	}
	if string(got) != "new content" {
		t.Errorf("expected 'new content', got %q", got)
	}
	assertNoLeftoverTempFiles(t, dir)
}

// TestEnsureWithPlantedTmpSymlinkDoesNotClobberVictim is a regression test
// for the TOCTOU/symlink vulnerability where updates were written through
// the predictable path+".tmp" location: a local attacker able to write the
// target's directory could plant path+".tmp" as a symlink to a victim file
// and have a privileged apply overwrite the victim. The random
// os.CreateTemp name plus atomic rename must leave the victim untouched.
func TestEnsureWithPlantedTmpSymlinkDoesNotClobberVictim(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("victim data"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.conf")
	if err := os.Symlink(victim, target+".tmp"); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(target, WithContent("PWNED")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("reading victim: %v", err)
	}
	if string(got) != "victim data" {
		t.Errorf("victim was clobbered: got %q, want 'victim data'", got)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if string(got) != "PWNED" {
		t.Errorf("target content: got %q, want 'PWNED'", got)
	}
	// The write must land as a regular file, never through a symlink.
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("target is still a symlink; expected a regular file")
	}
	// The planted symlink is unrelated to the write and must survive as-is.
	if _, err := os.Lstat(target + ".tmp"); err != nil {
		t.Errorf("planted symlink should remain untouched: %v", err)
	}
}

// TestEnsureWithExistingSymlinkTargetDoesNotFollowIt guards against future
// "simplify to a direct write" changes reintroducing a write-through-symlink
// path: an attacker- or admin-planted symlink AT the target path must be
// replaced by a regular file rather than written through. Unlike the planted
// tmp-symlink test above, the OLD implementation also passed this one (its
// rename already replaced the entry); it is a regression guard, not a
// vulnerability demo.
func TestEnsureWithExistingSymlinkTargetDoesNotFollowIt(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("victim data"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.conf")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(target, WithContent("PWNED")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("reading victim: %v", err)
	}
	if string(got) != "victim data" {
		t.Errorf("victim was clobbered through symlinked target: got %q", got)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("target symlink should have been replaced by a regular file")
	}
}

// TestEnsureUnchangedContentDoesNotChmodThroughSymlink is the regression
// test for the symlink-following vulnerability in attribute application:
// with unchanged content the target is never rewritten, and the old
// applyAttributesTo chmod'ed/chown'ed through the target PATH, following a
// planted symlink. An attacker able to write the target's directory could
// thus make a root-run apply re-permission an arbitrary victim file outside
// the managed directory (verified empirically: 0600 -> 0640 through the
// link). The fix treats a symlink at the target as "needs replacement" and
// applies attributes through an O_NOFOLLOW descriptor, so the victim must
// remain untouched and the target must become a regular managed file.
func TestEnsureUnchangedContentDoesNotChmodThroughSymlink(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	const content = "managed content"
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.conf")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}

	// Content matches exactly; only the planted symlink makes it "changed".
	if err := Ensure(target, WithContent(content)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// (i) The victim's permissions must be untouched: no chmod through the
	// symlink (the old code changed them 0o600 -> 0o640).
	victimInfo, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if victimInfo.Mode().Perm() != 0o600 {
		t.Errorf("victim perms changed through symlink: got %v, want 0o600", victimInfo.Mode().Perm())
	}
	// (ii) Victim content unchanged.
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("victim content changed: got %q, want %q", got, content)
	}
	// (iii) The symlink must have been replaced by a regular file.
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("target is still a symlink; expected it replaced by a regular file")
	}
	// (iv) The target carries the managed content.
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if string(got) != content {
		t.Errorf("target content: got %q, want %q", got, content)
	}
	// (v) The target carries the default managed mode.
	if info.Mode().Perm() != 0o640 {
		t.Errorf("target mode: got %v, want 0o640", info.Mode().Perm())
	}
}

// ensureWithTimeout runs Ensure in a goroutine and fails the test when it
// does not return within 5s: a planted FIFO at the target must never block
// the apply (the old getChecksum read-open had no O_NONBLOCK and hung until
// a writer appeared), and a hung goroutine would otherwise only surface via
// go test's 10-minute package timeout.
func ensureWithTimeout(t *testing.T, target string, opts ...FileOption) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- Ensure(target, opts...) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure hung on the planted FIFO (blocking open or read)")
		return nil // unreachable: t.Fatal ends the test goroutine
	}
}

// TestEnsureReplacesFifoAtTarget pins the FIFO hang fix: the checksum read
// used to os.ReadFile the target, whose read-open (no O_NONBLOCK) blocks
// indefinitely on a planted FIFO, hanging a root-run apply until a writer
// appears. ensureFile now classifies the target with Lstat first and counts
// a non-regular entry as needing replacement, so Ensure must return
// promptly and leave a regular managed file in the FIFO's place.
func TestEnsureReplacesFifoAtTarget(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Skipf("cannot create a FIFO on this filesystem: %v", err)
	}
	const content = "managed content"

	if err := ensureWithTimeout(t, target, WithContent(content)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("target is %v, want a regular file (FIFO must be replaced)", info.Mode())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if string(got) != content {
		t.Errorf("target content: got %q, want %q", got, content)
	}
	assertNoLeftoverTempFiles(t, dir)
}

// TestEnsureDryRunWithFifoAtTargetPreviewsWouldChange pins the dry-run
// behavior for a planted FIFO: the Lstat-first classification happens
// before any dry-run branch, so the run must preview a would-change note
// WITHOUT ever opening the FIFO (no hang), and the dry-run must mutate
// nothing — the FIFO still sits at the target afterwards.
func TestEnsureDryRunWithFifoAtTargetPreviewsWouldChange(t *testing.T) {
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Skipf("cannot create a FIFO on this filesystem: %v", err)
	}

	if err := ensureWithTimeout(t, target, WithContent("managed content")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	if !strings.Contains(buf.String(), "would-change File["+target+"]") {
		t.Errorf("expected a would-change note for %s, summary:\n%s", target, buf.String())
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("the FIFO must still exist after a dry-run: %v", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("dry-run must not touch the FIFO; target is now %v", info.Mode())
	}
}

// TestWithLineEditOnFifoTargetFailsLoudly pins the line-edit half of the
// FIFO hang fix: resolveLine used to os.ReadFile the target BEFORE
// ensureFile could classify it, so a planted FIFO at the target blocked a
// WithLine/WithoutLine apply indefinitely (until a writer appears) — the
// same DoS class the content path's Lstat-first fix closed. A line edit
// manages regular files and has no "replace with content" semantics, so a
// non-regular entry at the target is a loud user error naming the path and
// the entry type — and the planted FIFO must still be there afterwards.
func TestWithLineEditOnFifoTargetFailsLoudly(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "tmux.conf")
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Skipf("cannot create a FIFO on this filesystem: %v", err)
	}

	err := ensureWithTimeout(t, target, WithLine("source-file rocky.conf"))
	if err == nil {
		t.Fatal("expected a loud error for a line edit on a FIFO target")
	}
	if !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "FIFO") {
		t.Errorf("error should name the path and the FIFO, got: %v", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the planted FIFO must be untouched; target is now %v", info.Mode())
	}
}

// TestWithLineEditOnFifoTargetDryRunFailsLoudly pins the dry-run behavior:
// resolveLine runs before ensureFile's dry-run gating, so the line-edit
// policy error surfaces in a dry-run too — a loud preview failure instead
// of the old hang (the former plain os.ReadFile blocked identically in a
// dry-run). The dry-run must mutate nothing: the FIFO still sits at the
// target afterwards.
func TestWithLineEditOnFifoTargetDryRunFailsLoudly(t *testing.T) {
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	dir := t.TempDir()
	target := filepath.Join(dir, "tmux.conf")
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Skipf("cannot create a FIFO on this filesystem: %v", err)
	}

	err := ensureWithTimeout(t, target, WithLine("source-file rocky.conf"))
	if err == nil {
		t.Fatal("expected the dry-run preview to fail loudly on a FIFO target")
	}
	if !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "FIFO") {
		t.Errorf("error should name the path and the FIFO, got: %v", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("the FIFO must still exist after the dry-run: %v", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("dry-run must not touch the FIFO; target is now %v", info.Mode())
	}
}

// TestWithLineEditOnSymlinkToFifoFailsLoudly covers the backstop behind the
// Lstat policy's symlink exemption: the non-blocking open follows the
// symlink onto the FIFO without hanging, and the fstat of the OPENED entry
// refuses the non-regular target before a single byte is read (a plain
// EAGAIN check alone would misread a writer-less FIFO as an empty file).
func TestWithLineEditOnSymlinkToFifoFailsLoudly(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO on this filesystem: %v", err)
	}
	target := filepath.Join(dir, "tmux.conf")
	if err := os.Symlink(fifo, target); err != nil {
		t.Fatal(err)
	}

	err := ensureWithTimeout(t, target, WithLine("source-file rocky.conf"))
	if err == nil {
		t.Fatal("expected a loud error for a line edit following a symlink onto a FIFO")
	}
	if !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "FIFO") {
		t.Errorf("error should name the path and the FIFO, got: %v", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the refused read must not replace the symlink; target is now %v", info.Mode())
	}
}

// TestWithLineEditFollowsSymlinkTargetThenReplacesIt pins the PRESERVED
// symlink semantics of line edits: the read follows a symlink at the target
// exactly like the former os.ReadFile did (regular targets read
// identically through the O_NONBLOCK open), the line edit applies to the
// TARGET's content, and ensureFile then replaces the symlink with the
// regular managed file, matching its replace rule. The victim behind the
// link keeps its original content.
func TestWithLineEditFollowsSymlinkTargetThenReplacesIt(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	victim := filepath.Join(dir, "data.conf")
	if err := os.WriteFile(victim, []byte("keep\nold-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "tmux.conf")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(target, WithoutLine("old-line"), WithLine("new-line")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Errorf("target should have been replaced by a regular file, got %v", info.Mode())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep\nnew-line\n" {
		t.Errorf("line edit must apply to the symlink target's content, got %q", got)
	}
	kept, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "keep\nold-line\n" {
		t.Errorf("victim content must stay untouched, got %q", kept)
	}
}

// TestWithSourceFifoFailsLoudly pins the source-path half of the FIFO fix
// (resolveFromSourceOrContent): the source is a recipe-declared path read
// with the Lstat/non-blocking-open guard, so a planted FIFO at the source
// must produce a loud error naming the path instead of hanging the apply.
// The same read guards dir's source-tree copies, which delegate every file
// to file.Ensure with WithSource.
func TestWithSourceFifoFailsLoudly(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "source.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO on this filesystem: %v", err)
	}
	target := filepath.Join(dir, "target.conf")

	err := ensureWithTimeout(t, target, WithSource(fifo))
	if err == nil {
		t.Fatal("expected a loud error for a FIFO at the source path")
	}
	if !strings.Contains(err.Error(), fifo) || !strings.Contains(err.Error(), "FIFO") {
		t.Errorf("error should name the source path and the FIFO, got: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target must not be created from a FIFO source: %v", err)
	}
	info, err := os.Lstat(fifo)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the planted FIFO must be untouched; source is now %v", info.Mode())
	}
}

// TestEnsureReplacesUnixSocketAtTarget covers a second entry type of the
// same classification rule: a unix socket at the target is not a managed
// regular file either and must be replaced wholesale by the managed regular
// file (the classification is uniform for every non-regular entry type; the
// planted entry is never opened).
func TestEnsureReplacesUnixSocketAtTarget(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.sock")
	ln, err := net.Listen("unix", target)
	if err != nil {
		t.Skipf("cannot create a unix socket here: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if err := Ensure(target, WithContent("managed content")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("target is %v, want a regular file (socket must be replaced)", info.Mode())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "managed content" {
		t.Errorf("target content: got %q", got)
	}
	assertNoLeftoverTempFiles(t, dir)
}

// TestApplyAttributesToRefusesSymlinkAtTarget pins the kernel-level guard
// in applyAttributesTo: the target must be opened with O_NOFOLLOW so the
// kernel refuses (ELOOP) to open through a symlink planted at the path,
// leaving the victim behind it untouched.
func TestApplyAttributesToRefusesSymlinkAtTarget(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("victim data"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.conf")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}

	// Empty user/group on the literal File: uid/gid stay -1 (no-op chown).
	err := (&File{mode: 0o640}).applyAttributesTo(target)
	if err == nil {
		t.Fatal("expected applyAttributesTo to refuse a symlink at the target")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("error should mention the target path: %v", err)
	}

	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("victim perms changed: got %v, want 0o600", info.Mode().Perm())
	}
}

// TestApplyAttributesToFallbackOnOwnerUnreadable pins the path-based
// fallback for owner-unreadable modes: POSIX denies the owner O_RDONLY on a
// file whose mode lacks owner-read (e.g. 0o000), so a non-root run cannot
// open its own file for the fd-based attribute application; applyAttributesTo
// must fall back to path-based chown/chmod (which do not need open access)
// instead of erroring with a spurious EACCES. Skipped as root:
// CAP_DAC_OVERRIDE lets root's O_RDONLY open succeed, so the fallback never
// triggers there (and cannot be exercised).
func TestApplyAttributesToFallbackOnOwnerUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("fallback only triggers for non-root: root's CAP_DAC_OVERRIDE lets the O_NOFOLLOW open succeed")
	}
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.conf")
	if err := os.WriteFile(path, []byte("managed content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}

	// Empty user/group on the literal File: uid/gid stay -1 (no-op chown).
	if err := (&File{mode: 0o600}).applyAttributesTo(path); err != nil {
		t.Fatalf("applyAttributesTo failed on an owner-unreadable file: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("target changed type: got %v", info.Mode())
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode not applied via fallback: got %v, want 0o600", got)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "managed content" {
		t.Errorf("content changed: got %q", content)
	}
}

func TestConcurrentEnsureWritersDoNotInterfere(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "shared.conf")
	const writers = 8
	contents := make([]string, writers)
	for i := range contents {
		contents[i] = fmt.Sprintf("writer-%d\n", i)
	}

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each writer relies on its own unpredictable CreateTemp name.
			if err := Ensure(target, WithContent(contents[i])); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	found := false
	for _, c := range contents {
		if string(got) == c {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("target content %q is none of the written contents (torn write?)", got)
	}
	assertNoLeftoverTempFiles(t, dir)
}

// TestAtomicWriteCleansUpWhenRenameFails pins the error-path guarantee: if
// the final rename cannot succeed (here because a directory occupies the
// target path), the temporary file must be removed.
func TestAtomicWriteCleansUpWhenRenameFails(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	// A directory at the target path makes os.Rename fail.
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}

	err := atomicWrite(target, []byte("content"), 0o640)
	if err == nil {
		t.Fatal("expected an error when the target path is a directory")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("error should mention the target path: %v", err)
	}
	assertNoLeftoverTempFiles(t, dir)
}

// TestAtomicWriteLongBaseName proves temp names stay within NAME_MAX for
// base names that are themselves legal but leave little room for a suffix
// (the old path+".tmp" scheme also fit; the longer ".gonftmp" marker would
// not without truncating the base).
func TestAtomicWriteLongBaseName(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	// 250 chars + ".conf": legal as a plain file, but 250+8+10 random
	// characters would exceed NAME_MAX (255) without base truncation.
	base := strings.Repeat("l", 250)
	target := filepath.Join(dir, base)
	// Probe whether the target name itself is creatable on this filesystem;
	// skip only if even a plain file of that name exceeds NAME_MAX here.
	probe, err := os.Create(target)
	if err != nil {
		if strings.Contains(err.Error(), "file name too long") {
			t.Skip("filesystem NAME_MAX too small for this test")
		}
		t.Fatalf("probing NAME_MAX: %v", err)
	}
	_ = probe.Close()
	_ = os.Remove(target)

	if err := atomicWrite(target, []byte("long name content"), 0o640); err != nil {
		t.Fatalf("atomicWrite with 250-char base name: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if string(got) != "long name content" {
		t.Errorf("unexpected content %q", got)
	}
	assertNoLeftoverTempFiles(t, dir)
}

func assertNoLeftoverTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".gonftmp") {
			t.Errorf("leftover temporary file %s", e.Name())
		}
	}
}

// fileUIDGid returns the file's owning uid/gid via syscall.Stat_t. Skips the
// test when the platform does not expose Stat_t (repo targets unix only).
func fileUIDGid(t *testing.T, path string) (int, int) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skipf("no syscall.Stat_t on this platform")
	}
	return int(st.Uid), int(st.Gid)
}

// currentOwnerForTest resolves the current user and its group name so tests
// can chown to self unprivileged. Skips when either lookup fails.
func currentOwnerForTest(t *testing.T) (uname, gidStr, gname string) {
	t.Helper()
	curr, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	g, err := user.LookupGroupId(curr.Gid)
	if err != nil {
		t.Skipf("current gid %s has no group name: %v", curr.Gid, err)
	}
	return curr.Username, curr.Gid, g.Name
}

// TestEnsureGroupByNameResolvesViaLookupGroup pins the group-name resolution
// upgrade: WithGroup accepts a group name (resolved via os/user when the
// value is not numeric) as well as a numeric gid, and chowns to the same gid
// either way.
func TestEnsureGroupByNameResolvesViaLookupGroup(t *testing.T) {
	resource.ResetRepository()
	_, gidStr, gname := currentOwnerForTest(t)
	wantGid, err := strconv.Atoi(gidStr)
	if err != nil {
		t.Fatalf("parse gid %s: %v", gidStr, err)
	}

	dir := t.TempDir()
	byName := filepath.Join(dir, "byname.txt")
	if err := Ensure(byName, WithContent("x"), WithGroup(gname)); err != nil {
		t.Fatalf("Ensure with group name %s: %v", gname, err)
	}
	if _, got := fileUIDGid(t, byName); got != wantGid {
		t.Errorf("group name %s resolved to gid %d, want %d", gname, got, wantGid)
	}

	byNum := filepath.Join(dir, "bynum.txt")
	if err := Ensure(byNum, WithContent("x"), WithGroup(gidStr)); err != nil {
		t.Fatalf("Ensure with numeric gid %s: %v", gidStr, err)
	}
	if _, got := fileUIDGid(t, byNum); got != wantGid {
		t.Errorf("numeric gid %s applied as %d, want %d", gidStr, got, wantGid)
	}
}

// TestEnsureOwnerApplied pins that WithOwner (explicit user name) is applied
// to the managed file. Chowning to the current user's own uid works without
// privileges.
func TestEnsureOwnerApplied(t *testing.T) {
	resource.ResetRepository()
	uname, _, _ := currentOwnerForTest(t)
	u, err := user.Lookup(uname)
	if err != nil {
		t.Fatalf("lookup %s: %v", uname, err)
	}
	wantUID, err := strconv.Atoi(u.Uid)
	if err != nil {
		t.Fatalf("parse uid %s: %v", u.Uid, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "owned.txt")
	if err := Ensure(path, WithContent("x"), WithOwner(uname)); err != nil {
		t.Fatalf("Ensure with owner %s: %v", uname, err)
	}
	gotUID, _ := fileUIDGid(t, path)
	if gotUID != wantUID {
		t.Errorf("owner %s applied uid %d, want %d", uname, gotUID, wantUID)
	}
}

// TestEnsureUnknownGroupFails pins the error path of the group resolution:
// an unresolvable group name must fail the apply loudly (it used to fail
// with a numeric-only error).
func TestEnsureUnknownGroupFails(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "badgroup.txt")
	err := Ensure(path, WithContent("x"), WithGroup("gonf-no-such-group-8f3a"))
	if err == nil || !strings.Contains(err.Error(), "failed to resolve group") {
		t.Fatalf("expected group resolution error, got %v", err)
	}
}

func TestPresentStringCreateNewFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	Present(path, WithContent("hello world"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestPresentMode(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "mode.txt")
	mode := os.FileMode(0o600)

	Present(path, WithContent("mode test"), WithMode(mode))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("expected mode %v, got %v", mode, info.Mode().Perm())
	}
}

// TestEnsureUnchangedContentStillAppliesModeChange checks the fd-based
// attribute path on a plain regular file: content already matches, but a
// different WithMode must still be applied to the existing inode.
func TestEnsureUnchangedContentStillAppliesModeChange(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "mode-change.conf")
	if err := os.WriteFile(path, []byte("stable content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(path, WithContent("stable content"), WithMode(0o600)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected mode 0o600 on unchanged content, got %v", info.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "stable content" {
		t.Errorf("content changed: got %q", got)
	}
}

func TestPresentSourceFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	sourcePath := filepath.Join("..", "..", "assets", "testfiles", "test.txt")
	targetPath := filepath.Join(dir, "target.txt")

	Present(targetPath, WithSource(sourcePath))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	expected, _ := os.ReadFile(sourcePath)
	if string(got) != string(expected) {
		t.Errorf("expected %q, got %q", string(expected), string(got))
	}
}

func TestPresentTemplateFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	sourcePath := filepath.Join("..", "..", "assets", "testfiles", "test.tmpl")
	targetPath := filepath.Join(dir, "target.conf")

	Present(targetPath, WithSource(sourcePath))
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	expected := "Hello, " + sourcePath + "!\nWelcome to " + os.Getenv("USER") + ".\n"
	if string(got) != expected {
		t.Errorf("expected %q, got %q", expected, string(got))
	}
}

func TestPresentAbsent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	Present(path, IsAbsent)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", path)
	}

	// Idempotent: removing a missing file is not an error (direct call to
	// avoid duplicate registration in the one-per-process registry).
	if err := ensureAbsent(path); err != nil {
		t.Fatalf("absent on missing file: %v", err)
	}
}

func TestAbsent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	Absent(path)
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", path)
	}

	// Idempotent: removing a missing file is not an error (direct call to
	// avoid duplicate registration in the one-per-process registry).
	if err := ensureAbsent(path); err != nil {
		t.Fatalf("absent on missing file: %v", err)
	}
}

func TestResolveStripsTmplSuffixWhenSourceHasTmplSuffix(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "foo.conf.tmpl")
	if err := os.WriteFile(sourcePath, []byte("hello {{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Mirrors what dir's copySourceTree mechanically passes: a target path
	// that still carries the source's own ".tmpl" suffix.
	dstDir := filepath.Join(dir, "dst")
	if err := os.Mkdir(dstDir, 0o750); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(dstDir, "foo.conf.tmpl")

	if err := Ensure(targetPath, WithSource(sourcePath)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deSuffixed := filepath.Join(filepath.Dir(targetPath), "foo.conf")
	if _, err := os.Stat(deSuffixed); err != nil {
		t.Errorf("expected de-suffixed file to exist at %s: %v", deSuffixed, err)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Errorf("expected %s to NOT exist on disk", targetPath)
	}
}

func TestResolveDoesNotStripTmplWhenOnlyContentTriggersTemplate(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "foo.conf.tmpl")

	if err := Ensure(targetPath, WithContent("hello {{.Param}}")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(targetPath); err != nil {
		t.Errorf("expected %s to exist (not stripped), got %v", targetPath, err)
	}
}

func TestParamIsBareSourcePathNoPrefix(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "src.tmpl")
	if err := os.WriteFile(sourcePath, []byte("{{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(dir, "dst.conf")

	if err := Ensure(targetPath, WithSource(sourcePath)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != sourcePath {
		t.Errorf("expected Param to equal bare source path %q, got %q", sourcePath, string(got))
	}
	if strings.Contains(string(got), "source://") {
		t.Errorf("Param unexpectedly contains the removed \"source://\" prefix: %q", string(got))
	}
}

func TestWithLineCreatesMissingFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux.conf")

	if err := Ensure(path, WithLine("source-file rocky.conf")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "source-file rocky.conf\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWithLineIdempotent(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux.conf")
	initial := "a\nsource-file rocky.conf\nb\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(path, WithLine("source-file rocky.conf")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != initial {
		t.Fatalf("content changed: %q", got)
	}
}

func TestWithLineAppends(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(path, []byte("set -g prefix C-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(path, WithLine("source-file rocky.conf")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "set -g prefix C-a\nsource-file rocky.conf\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWithoutLineRemoves(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux.local.conf")
	if err := os.WriteFile(path, []byte("x\nsource-file rocky.conf\ny\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(path, WithoutLine("source-file rocky.conf")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "x\ny\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWithoutLineMissingFile(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.conf")

	if err := Ensure(path, WithoutLine("source-file rocky.conf")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file should not have been created")
	}
}

func TestWithLineAndWithoutLineReplace(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("keep\nold-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(path, WithoutLine("old-line"), WithLine("new-line")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep\nnew-line\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWithLinesBatchesInOrderAndDeduplicates(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "lines.conf")
	if err := os.WriteFile(path, []byte("keep\nold\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path,
		WithoutLines("old", "old", "stale"),
		WithoutLine("stale"),
		WithLines("second", "first", "second"),
		WithLine("third"),
	); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "keep\nsecond\nfirst\nthird\n"; string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestPresentDefaultIDStillUsesTargetPath(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "default-id.conf")
	got := Present(path, WithLine("enabled=1"))
	if want := "File[" + path + "]"; got.ID() != want {
		t.Fatalf("default File ID = %q, want %q", got.ID(), want)
	}
}

func TestNamedFileContentReportsNamedID(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "named-content.conf")
	res := Present(path, WithName("named-content"), WithContent("managed\n"))
	if got, want := res.ID(), "File[named-content]"; got != want {
		t.Fatalf("ID = %q, want %q", got, want)
	}
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "managed\n" {
		t.Fatalf("content = %q, %v", got, err)
	}
	if !resource.AnyChanged(res.ID()) {
		t.Fatalf("named content file did not report change as %s", res.ID())
	}
}

func TestNamedAbsentFileReportsNamedID(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "named-absent.conf")
	if err := os.WriteFile(path, []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := Present(path, WithName("named-absent"), IsAbsent)
	if got, want := res.ID(), "File[named-absent]"; got != want {
		t.Fatalf("ID = %q, want %q", got, want)
	}
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("removed file stat = %v, want not exist", err)
	}
	if !resource.AnyChanged(res.ID()) {
		t.Fatalf("named absent file did not report change as %s", res.ID())
	}
}

func TestNamedEnsureFileReportsNamedID(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "named-marker")
	res := PresentEnsure(path, WithName("named-marker"))
	if got, want := res.ID(), "EnsureFile[named-marker]"; got != want {
		t.Fatalf("ID = %q, want %q", got, want)
	}
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("ensured file stat: %v", err)
	}
	if !resource.AnyChanged(res.ID()) {
		t.Fatalf("named EnsureFile did not report change as %s", res.ID())
	}
}

func TestPlanApplyForwardsNamedFileIdentity(t *testing.T) {
	resource.ResetRepository()
	base := t.TempDir()
	contentPath := filepath.Join(base, "content.conf")
	absentPath := filepath.Join(base, "absent.conf")
	ensurePath := filepath.Join(base, "marker")
	if err := os.WriteFile(absentPath, []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const (
		contentID = "File[named-plan-content]"
		absentID  = "File[named-plan-absent]"
		ensureID  = "EnsureFile[named-plan-marker]"
	)
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "named-file-forwarding"},
		{
			Op:         plan.KindFile,
			ID:         contentID,
			Name:       "named-plan-content",
			Path:       contentPath,
			ContentB64: base64.StdEncoding.EncodeToString([]byte("managed\n")),
			HasContent: true,
		},
		{
			Op:     plan.KindFile,
			ID:     absentID,
			Name:   "named-plan-absent",
			Path:   absentPath,
			Absent: true,
		},
		{
			Op:   plan.KindEnsureFile,
			ID:   ensureID,
			Name: "named-plan-marker",
			Path: ensurePath,
		},
	}
	if err := plan.Apply(ops, plan.Facts{}, base); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, err := os.ReadFile(contentPath); err != nil || string(got) != "managed\n" {
		t.Fatalf("content = %q, %v", got, err)
	}
	if _, err := os.Lstat(absentPath); !os.IsNotExist(err) {
		t.Fatalf("removed file stat = %v, want not exist", err)
	}
	if _, err := os.Lstat(ensurePath); err != nil {
		t.Fatalf("ensured file stat: %v", err)
	}
	for _, id := range []string{contentID, absentID, ensureID} {
		if !resource.AnyChanged(id) {
			t.Errorf("named plan operation did not report change as %s", id)
		}
	}
}

func TestNamedFileDuplicateRegistrationFails(t *testing.T) {
	if os.Getenv("GONF_FILE_DUPLICATE_HELPER") == "1" {
		resource.ResetRepository()
		Present("/tmp/gonf-named-duplicate", WithName("duplicate"), WithLine("one"))
		Present("/tmp/gonf-named-duplicate", WithName("duplicate"), WithLine("two"))
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestNamedFileDuplicateRegistrationFails$")
	cmd.Env = append(os.Environ(), "GONF_FILE_DUPLICATE_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("duplicate named file registration unexpectedly succeeded")
	}
	if !strings.Contains(string(output), "already registered") {
		t.Fatalf("duplicate registration output = %q, want already registered", output)
	}
}

func TestPresentWithNameAllowsOrderedLineEditsToOnePath(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "rc.conf.local")

	packages := Present(path,
		WithName("rc-conf-package-scripts"),
		WithLine(`pkg_scripts="dtail"`),
	)
	services := Present(path,
		WithName("rc-conf-httpd-flags"),
		WithLine(`httpd_flags=""`),
		DependsOn(packages),
	)
	if got, want := packages.ID(), "File[rc-conf-package-scripts]"; got != want {
		t.Fatalf("package line ID = %q, want %q", got, want)
	}
	if got, want := services.ID(), "File[rc-conf-httpd-flags]"; got != want {
		t.Fatalf("service line ID = %q, want %q", got, want)
	}

	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pkg_scripts=\"dtail\"\nhttpd_flags=\"\"\n"; string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if !resource.AnyChanged(packages.ID()) || !resource.AnyChanged(services.ID()) {
		t.Fatalf("named line edits must report their own IDs: packages=%t services=%t",
			resource.AnyChanged(packages.ID()), resource.AnyChanged(services.ID()))
	}
}

func TestEnsurePresentPreservesExistingContentAndConvergesExplicitMode(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "daily.local")
	if err := os.WriteFile(path, []byte("keep this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePresent(path, WithMode(0o644)); err != nil {
		t.Fatalf("EnsurePresent: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep this\n" {
		t.Fatalf("content = %q, want preserved bytes", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestEnsurePresentRerunWithMatchingMetadataIsOK(t *testing.T) {
	resource.ResetRepository()
	userName, _, groupName := currentOwnerForTest(t)
	path := filepath.Join(t.TempDir(), "daily.local")
	if err := os.WriteFile(path, []byte("keep this\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	resource.ResetReport()
	if err := EnsurePresent(path, WithMode(0o640), WithOwner(userName), WithGroup(groupName)); err != nil {
		t.Fatalf("EnsurePresent: %v", err)
	}
	if resource.AnyChanged("EnsureFile[" + path + "]") {
		t.Fatal("matching explicit metadata should report OK, not changed")
	}

	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	resource.ResetReport()
	if err := EnsurePresent(path, WithMode(0o640), WithOwner(userName), WithGroup(groupName)); err != nil {
		t.Fatalf("EnsurePresent dry-run: %v", err)
	}
	if resource.AnyChanged("EnsureFile[" + path + "]") {
		t.Fatal("matching metadata should remain OK in dry-run")
	}
}

func TestEnsurePresentCreatesMissingAndDryRunDoesNotMutate(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	missing := filepath.Join(dir, "rc.local")
	if err := EnsurePresent(missing); err != nil {
		t.Fatalf("EnsurePresent missing: %v", err)
	}
	got, err := os.ReadFile(missing)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("new file = %q, want empty", got)
	}

	dry := filepath.Join(dir, "dry.local")
	resource.ResetReport()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })
	if err := EnsurePresent(dry, WithMode(0o600)); err != nil {
		t.Fatalf("EnsurePresent dry-run: %v", err)
	}
	if _, err := os.Lstat(dry); !os.IsNotExist(err) {
		t.Fatalf("dry-run created file: %v", err)
	}
	if !resource.AnyChanged("EnsureFile[" + dry + "]") {
		t.Fatal("dry-run did not report the pending file creation")
	}
}

// TestAbsentDoesNotMutateCallerOptionSlice guards against 100 Go Mistakes
// #25: Absent used to append IsAbsent onto the caller-owned variadic slice,
// writing into the spare capacity of a reusable option list and silently
// turning later Present calls built from the same backing array into
// deletions.
func TestAbsentDoesNotMutateCallerOptionSlice(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.txt")
	keep := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(gone, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("stay"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reusable option list with spare capacity (len 2, cap 8). Sub-slices
	// share its backing array.
	base := make([]FileOption, 2, 8)
	base[0] = WithContent("stay")
	base[1] = WithMode(0o644)
	before := make([]FileOption, len(base))
	copy(before, base)

	Absent(gone, base[:1]...)
	Present(keep, base[:2]...)

	// (a) The caller-owned backing array must be unchanged.
	for i := range base {
		if reflect.ValueOf(base[i]).Pointer() != reflect.ValueOf(before[i]).Pointer() {
			t.Fatalf("caller option slice mutated at index %d: IsAbsent was injected into the caller's backing array", i)
		}
	}

	// (b) The Present resource built from the same backing array must not
	// have become absent.
	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	data, err := os.ReadFile(keep)
	if err != nil {
		t.Fatalf("expected %s to still exist: %v", keep, err)
	}
	if string(data) != "stay" {
		t.Errorf("keep.txt content = %q, want %q", data, "stay")
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", gone)
	}
}

// TestEnsureLineEditConflictsWithContent pins the build()-side validation:
// the conflicting option combination must surface as a RETURNED error (apply
// time, e.g. from plan apply) instead of exiting the process; Present keeps
// the fail-fast Fatal for record-time recipe misuse.
func TestEnsureLineEditConflictsWithContent(t *testing.T) {
	resource.ResetRepository()
	path := filepath.Join(t.TempDir(), "conflict.txt")

	err := Ensure(path, WithLine("one"), WithContent("two"))
	if err == nil {
		t.Fatal("expected WithLine + WithContent to be rejected with an error")
	}
	if !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("error should name the conflicting options, got: %v", err)
	}
}

// TestResolveGroupIDRejectsNegativeGid pins the loud rejection of a negative
// gid (task 022): chown(uid, -1) would silently leave the group unchanged.
func TestResolveGroupIDRejectsNegativeGid(t *testing.T) {
	if _, err := resolveGroupID("-1"); err == nil || !strings.Contains(err.Error(), "invalid gid -1") {
		t.Fatalf("expected an invalid-gid error, got %v", err)
	}
}

// TestApplyAttributesToRejectsNegativeGroupGid runs the full attribute path
// with WithGroup("-1") and asserts a loud error instead of a silent no-op.
func TestEnsureRejectsNegativeGroup(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(target, WithContent("x"), WithGroup("-1")); err == nil ||
		!strings.Contains(err.Error(), "invalid gid -1") {
		t.Fatalf("expected the negative-gid error, got %v", err)
	}
}

// TestWithParamOverridesTemplateParam pins the WithParam override (task 622):
// a caller that knows a more stable identity than the mechanical source path
// (dir's plan-path tree copies) can override the {{.Param}} value rendered
// into template content; without it the derived default stays in place.
func TestWithParamOverridesTemplateParam(t *testing.T) {
	resource.ResetRepository()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "app.conf.tmpl")
	if err := os.WriteFile(sourcePath, []byte("param is {{.Param}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(dir, "app.conf")

	if err := Ensure(targetPath, WithSource(sourcePath), WithParam("assets/testfiles/app.conf.tmpl")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if want := "param is assets/testfiles/app.conf.tmpl\n"; string(got) != want {
		t.Errorf("expected Param override to render %q, got %q", want, got)
	}
	if strings.Contains(string(got), dir) {
		t.Errorf("rendered content must not embed the mechanical source path %q: %q", sourcePath, got)
	}
}
