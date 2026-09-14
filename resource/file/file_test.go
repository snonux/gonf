package file

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	. "github.com/snonux/gonf/api/options"
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
	probe.Close()
	os.Remove(target)

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
	base := make([]Option, 2, 8)
	base[0] = WithContent("stay")
	base[1] = WithMode(0o644)
	before := make([]Option, len(base))
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
