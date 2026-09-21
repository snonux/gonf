package file

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	. "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

// These tests pin the exact error text and ordering of the candidate
// validation helpers so refactors of validation.go stay behaviour-preserving.
// The parent-directory rules are driven through the validationLstat and
// validationGeteuid seams with synthetic owners and modes, so they are
// hermetic: they neither depend on the host's accounts nor on whether the
// ancestors of $TMPDIR would pass the checks themselves. None of the tests in
// this package run in parallel, so swapping the seams is race-free.

// fakeValidationEntry describes one synthetic path for fakeValidationFS.
type fakeValidationEntry struct {
	mode   os.FileMode
	uid    uint32
	noStat bool // Sys() returns no *syscall.Stat_t
}

// fakeValidationInfo is the os.FileInfo returned by the fake Lstat.
type fakeValidationInfo struct {
	name  string
	entry fakeValidationEntry
}

func (i fakeValidationInfo) Name() string       { return i.name }
func (i fakeValidationInfo) Size() int64        { return 0 }
func (i fakeValidationInfo) Mode() os.FileMode  { return i.entry.mode }
func (i fakeValidationInfo) ModTime() time.Time { return time.Time{} }
func (i fakeValidationInfo) IsDir() bool        { return i.entry.mode.IsDir() }
func (i fakeValidationInfo) Sys() any {
	if i.entry.noStat {
		return nil
	}
	return &syscall.Stat_t{Uid: i.entry.uid}
}

// dirEntry is a shorthand for a synthetic directory with perm and owner.
func dirEntry(perm os.FileMode, uid uint32) fakeValidationEntry {
	return fakeValidationEntry{mode: os.ModeDir | perm, uid: uid}
}

// fakeValidationFS makes the parent checks see only entries and run as euid.
// Paths missing from entries report ENOENT like a real Lstat.
func fakeValidationFS(t *testing.T, euid uint32, entries map[string]fakeValidationEntry) {
	t.Helper()
	prevLstat, prevEUID := validationLstat, validationGeteuid
	t.Cleanup(func() { validationLstat, validationGeteuid = prevLstat, prevEUID })
	validationGeteuid = func() int { return int(euid) }
	validationLstat = func(path string) (os.FileInfo, error) {
		entry, ok := entries[path]
		if !ok {
			return nil, &fs.PathError{Op: "lstat", Path: path, Err: syscall.ENOENT}
		}
		return fakeValidationInfo{name: filepath.Base(path), entry: entry}, nil
	}
}

// privateValidationDir returns a real 0700 directory owned by the test user
// whose ancestors (the $TMPDIR chain) are reported to the parent checks as
// trusted root-owned 0755 directories. The directory itself and everything
// below it are still inspected with the real os.Lstat.
func privateValidationDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	mkdirValidationMode(t, dir, 0o700)
	prevLstat := validationLstat
	t.Cleanup(func() { validationLstat = prevLstat })
	validationLstat = func(path string) (os.FileInfo, error) {
		if path == string(filepath.Separator) || strings.HasPrefix(dir, path+string(filepath.Separator)) {
			return fakeValidationInfo{name: filepath.Base(path), entry: dirEntry(0o755, 0)}, nil
		}
		return prevLstat(path)
	}
	return dir
}

// mkdirValidationMode creates dir with exactly perm, bypassing the process
// umask so the group/other write bits under test are really set.
func mkdirValidationMode(t *testing.T, dir string, perm os.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, perm); err != nil {
		t.Fatal(err)
	}
}

// wantValidationErr fails unless err's text is exactly want ("" means nil).
func wantValidationErr(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestVerifyValidationParentRejectsRelativePath(t *testing.T) {
	fakeValidationFS(t, 1000, nil)
	wantValidationErr(t, verifyValidationParent("etc/service"), "etc/service is not an absolute path")
}

func TestVerifyValidationParentRejectsParentComponentBeforeInspecting(t *testing.T) {
	fakeValidationFS(t, 1000, nil)
	wantValidationErr(t, verifyValidationParent("/missing/../etc"),
		"/missing/../etc contains a parent-directory path component")
}

// The filesystem root has no components, so it is checked by the dedicated
// root-only branch: it must be owned by the applying uid and not writable by
// group or other users, whatever spelling of "/" is used.
func TestVerifyValidationParentRootOnly(t *testing.T) {
	tests := []struct {
		name  string
		euid  uint32
		entry *fakeValidationEntry
		want  string
	}{
		{"trusted root", 0, &fakeValidationEntry{mode: os.ModeDir | 0o755}, ""},
		{"owned by applying non-root uid", 1000, &fakeValidationEntry{mode: os.ModeDir | 0o700, uid: 1000}, ""},
		{"owned by another uid", 1000, &fakeValidationEntry{mode: os.ModeDir | 0o755}, "/ is not owned by the applying uid"},
		{"group writable", 0, &fakeValidationEntry{mode: os.ModeDir | 0o775}, "/ is writable by group or other users"},
		{"world writable sticky", 0, &fakeValidationEntry{mode: os.ModeDir | os.ModeSticky | 0o757}, "/ is writable by group or other users"},
		{"no stat data", 0, &fakeValidationEntry{mode: os.ModeDir | 0o755, noStat: true}, "cannot verify ownership of /"},
		{"lstat failure", 0, nil, "inspect /: lstat /: no such file or directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := map[string]fakeValidationEntry{}
			if tt.entry != nil {
				entries["/"] = *tt.entry
			}
			fakeValidationFS(t, tt.euid, entries)
			for _, parent := range []string{"/", "//", "/./"} {
				wantValidationErr(t, verifyValidationParent(parent), tt.want)
			}
		})
	}
}

// trustedValidationChain is /srv (root-owned 0755) under a trusted root; the
// component tests below add the entries they vary.
func trustedValidationChain(extra map[string]fakeValidationEntry) map[string]fakeValidationEntry {
	entries := map[string]fakeValidationEntry{
		"/":    dirEntry(0o755, 0),
		"/srv": dirEntry(0o755, 0),
	}
	for path, entry := range extra {
		entries[path] = entry
	}
	return entries
}

func TestVerifyValidationParentLastComponentRules(t *testing.T) {
	tests := []struct {
		name  string
		euid  uint32
		entry fakeValidationEntry
		want  string
	}{
		{"private to applying uid", 1000, dirEntry(0o700, 1000), ""},
		{"readable by others", 1000, dirEntry(0o755, 1000), ""},
		{"root applying to root-owned", 0, dirEntry(0o755, 0), ""},
		{"root-owned for non-root applier", 1000, dirEntry(0o755, 0), "/srv/app is not owned by the applying uid"},
		{"foreign owner", 1000, dirEntry(0o700, 2000), "/srv/app is not owned by the applying uid"},
		{"group writable", 1000, dirEntry(0o720, 1000), "/srv/app is writable by group or other users"},
		{"other writable", 1000, dirEntry(0o702, 1000), "/srv/app is writable by group or other users"},
		{"sticky does not excuse", 1000, dirEntry(os.ModeSticky|0o777, 1000), "/srv/app is writable by group or other users"},
		{"not a directory", 1000, fakeValidationEntry{mode: 0o600, uid: 1000}, "/srv/app is not a directory"},
		{"symlink", 1000, fakeValidationEntry{mode: os.ModeSymlink | 0o777, uid: 1000}, "/srv/app contains a symlink path component"},
		{"no stat data", 1000, fakeValidationEntry{mode: os.ModeDir | 0o700, noStat: true}, "cannot verify ownership of /srv/app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeValidationFS(t, tt.euid, trustedValidationChain(map[string]fakeValidationEntry{"/srv/app": tt.entry}))
			wantValidationErr(t, verifyValidationParent("/srv/app"), tt.want)
		})
	}
}

// Intermediates must be real directories owned by root or the applying uid,
// and may be writable by others only with sticky protection (like /tmp).
func TestVerifyValidationParentIntermediateRules(t *testing.T) {
	tests := []struct {
		name  string
		entry fakeValidationEntry
		want  string
	}{
		{"root-owned", dirEntry(0o755, 0), ""},
		{"owned by applying uid", dirEntry(0o700, 1000), ""},
		{"sticky world writable", dirEntry(os.ModeSticky|0o777, 0), ""},
		{"foreign owner", dirEntry(0o755, 2000), "/srv/shared is owned by an untrusted uid"},
		{"world writable without sticky", dirEntry(0o777, 0), "/srv/shared is writable by group or other users without sticky protection"},
		{"group writable without sticky", dirEntry(0o720, 1000), "/srv/shared is writable by group or other users without sticky protection"},
		{"not a directory", fakeValidationEntry{mode: 0o644}, "/srv/shared is not a directory"},
		{"symlink", fakeValidationEntry{mode: os.ModeSymlink | 0o777}, "/srv/shared contains a symlink path component"},
		{"no stat data", fakeValidationEntry{mode: os.ModeDir | 0o755, noStat: true}, "cannot verify ownership of /srv/shared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeValidationFS(t, 1000, trustedValidationChain(map[string]fakeValidationEntry{
				"/srv/shared":     tt.entry,
				"/srv/shared/app": dirEntry(0o700, 1000),
			}))
			wantValidationErr(t, verifyValidationParent("/srv/shared/app"), tt.want)
		})
	}
}

// Components are checked top-down and the first failure wins; nothing below
// a rejected or missing component is inspected.
func TestVerifyValidationParentReportsFirstFailingComponent(t *testing.T) {
	fakeValidationFS(t, 1000, trustedValidationChain(map[string]fakeValidationEntry{
		"/srv/a":   dirEntry(0o755, 2000),
		"/srv/a/b": fakeValidationEntry{mode: os.ModeSymlink},
	}))
	wantValidationErr(t, verifyValidationParent("/srv/a/b/c"), "/srv/a is owned by an untrusted uid")

	fakeValidationFS(t, 1000, trustedValidationChain(nil))
	err := verifyValidationParent("/srv/missing/deeper")
	wantValidationErr(t, err, "inspect /srv/missing: lstat /srv/missing: no such file or directory")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing component error %v does not wrap os.ErrNotExist", err)
	}
}

// The real-filesystem cases use os.Lstat for the test directories themselves
// (only the $TMPDIR ancestry is reported as trusted).
func TestVerifyValidationParentRealDirectories(t *testing.T) {
	dir := privateValidationDir(t)
	wantValidationErr(t, verifyValidationParent(dir), "")

	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wantValidationErr(t, verifyValidationParent(regular), regular+" is not a directory")
	wantValidationErr(t, verifyValidationParent(filepath.Join(regular, "child")), regular+" is not a directory")

	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	wantValidationErr(t, verifyValidationParent(link), link+" contains a symlink path component")

	shared := filepath.Join(dir, "shared")
	mkdirValidationMode(t, shared, 0o777)
	mkdirValidationMode(t, filepath.Join(shared, "app"), 0o700)
	wantValidationErr(t, verifyValidationParent(filepath.Join(shared, "app")),
		shared+" is writable by group or other users without sticky protection")
}

func TestValidationParentErrorIsWrappedWithTarget(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "missing", "service.conf")
	err := Ensure(target, WithContent("candidate"), WithValidation("true", []string{CandidatePath}))
	want := "file " + target + ": validation candidate parent: inspect " + filepath.Dir(target) + ": "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("error = %v, want prefix %q", err, want)
	}
}

// CreateTemp itself is not faked, so this needs the kernel to enforce the
// directory's missing write bit; root bypasses that check.
func TestValidationCreateCandidateFailureIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permission, so CreateTemp cannot be made to fail")
	}
	resource.ResetRepository()
	parent := filepath.Join(privateValidationDir(t), "readonly")
	mkdirValidationMode(t, parent, 0o500)
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	target := filepath.Join(parent, "service.conf")
	err := Ensure(target, WithContent("candidate"), WithValidation("true", []string{CandidatePath}))
	want := "file " + target + ": create validation candidate: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("error = %v, want prefix %q", err, want)
	}
}

func TestValidationFailureMessageNamesValidator(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, "exit 3")
	err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath}))
	wantValidationErr(t, err, "file "+target+": validation by "+validator+" failed: exit status 3")
}

// When both the validator and the candidate cleanup fail, the validator
// error stays primary (and unwrappable) and the cleanup error is appended.
func TestValidationFailureAndCleanupFailureAreJoined(t *testing.T) {
	resource.ResetRepository()
	target := filepath.Join(privateValidationDir(t), "service.conf")
	validator := writeValidationScript(t, `rm "$1"
mkdir "$1"
touch "$1/leftover"
exit 4`)
	err := Ensure(target, WithContent("candidate"), WithValidation(validator, []string{CandidatePath}))
	if err == nil {
		t.Fatal("expected joined validation and cleanup error")
	}
	msg := err.Error()
	prefix := "file " + target + ": validation by " + validator + " failed: exit status 4; file " + target + ": remove validation candidate: remove "
	if !strings.HasPrefix(msg, prefix) {
		t.Fatalf("error = %q, want prefix %q", msg, prefix)
	}
	if !strings.Contains(msg, ".gonfvalidate") || !strings.HasSuffix(msg, ": directory not empty") {
		t.Fatalf("error = %q, want candidate removal failure", msg)
	}
	var exitErr interface{ ExitCode() int }
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 4 {
		t.Fatalf("validator exit error not unwrappable from %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("failed validation published live target: %v", statErr)
	}
}

// The validator receives the argv in order, with only the exact CandidatePath
// element substituted; lookalike literals (the placeholder text without its
// NUL delimiters, which exec could not pass anyway) go through verbatim.
func TestValidationArgvSubstitutesOnlyExactPlaceholder(t *testing.T) {
	resource.ResetRepository()
	dir := privateValidationDir(t)
	target := filepath.Join(dir, "service.conf")
	record := filepath.Join(dir, "argv")
	validator := writeValidationScript(t, `for a in "$@"; do printf '%s\n' "$a"; done > "`+record+`"`)
	args := []string{"-c", strings.Trim(CandidatePath, "\x00"), CandidatePath, "--", ""}
	if err := Ensure(target, WithContent("candidate"), WithValidation(validator, args)); err != nil {
		t.Fatalf("validated apply: %v", err)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != len(args) {
		t.Fatalf("argv = %q, want %d entries", lines, len(args))
	}
	for i, arg := range args {
		if i == 2 {
			if filepath.Dir(lines[i]) != dir || !strings.HasPrefix(filepath.Base(lines[i]), "service.conf.gonfvalidate") {
				t.Fatalf("argv[%d] = %q, want candidate beside target", i, lines[i])
			}
			continue
		}
		if lines[i] != arg {
			t.Fatalf("argv[%d] = %q, want %q", i, lines[i], arg)
		}
	}
}

func TestCandidateNamePatternTruncatesLongBase(t *testing.T) {
	if got := candidateNamePattern("/etc/nsd.conf"); got != "nsd.conf.gonfvalidate*" {
		t.Fatalf("short pattern = %q", got)
	}
	long := strings.Repeat("a", 300)
	got := candidateNamePattern("/etc/" + long)
	want := strings.Repeat("a", 255-len(".gonfvalidate")-10) + ".gonfvalidate*"
	if got != want {
		t.Fatalf("long pattern length = %d, want %d", len(got), len(want))
	}
}

func TestSubstituteCandidatePathCopiesAndReplacesWholeElements(t *testing.T) {
	in := []string{"-c", CandidatePath, "x" + CandidatePath, CandidatePath}
	orig := append([]string(nil), in...)
	got := substituteCandidatePath(in, "/etc/c")
	want := []string{"-c", "/etc/c", "x" + CandidatePath, "/etc/c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("substituted = %q, want %q", got, want)
	}
	if strings.Join(in, "|") != strings.Join(orig, "|") {
		t.Fatalf("input mutated to %q", in)
	}
	if got := substituteCandidatePath(nil, "/etc/c"); got == nil || len(got) != 0 {
		t.Fatalf("nil args = %#v, want empty non-nil slice", got)
	}
}

// fakeCandidateFile records the calls writeValidationCandidate makes and
// fails the configured one.
type fakeCandidateFile struct {
	failOn string
	calls  []string
}

func (f *fakeCandidateFile) step(name string) error {
	f.calls = append(f.calls, name)
	if f.failOn == name {
		return errors.New(name + " failed")
	}
	return nil
}

func (f *fakeCandidateFile) Name() string                { return "/etc/x.gonfvalidate0" }
func (f *fakeCandidateFile) Write(p []byte) (int, error) { return len(p), f.step("write") }
func (f *fakeCandidateFile) Sync() error                 { return f.step("sync") }
func (f *fakeCandidateFile) Close() error                { return f.step("close") }

// writeValidationCandidate stops at the first failure without closing the
// file itself (the deferred cleanup does that) and names the failing step.
func TestWriteValidationCandidateFailures(t *testing.T) {
	tests := []struct {
		failOn string
		calls  string
		want   string
	}{
		{"", "write,sync,close", ""},
		{"write", "write", "file /etc/x: write validation candidate: write failed"},
		{"sync", "write,sync", "file /etc/x: sync validation candidate: sync failed"},
		{"close", "write,sync,close", "file /etc/x: close validation candidate: close failed"},
	}
	for _, tt := range tests {
		t.Run("fail on "+tt.failOn, func(t *testing.T) {
			candidate := &fakeCandidateFile{failOn: tt.failOn}
			wantValidationErr(t, writeValidationCandidate("/etc/x", candidate, []byte("candidate")), tt.want)
			if got := strings.Join(candidate.calls, ","); got != tt.calls {
				t.Fatalf("calls = %q, want %q", got, tt.calls)
			}
		})
	}
}

// createCleanupCandidate opens a real candidate file for the cleanup tests.
func createCleanupCandidate(t *testing.T) *os.File {
	t.Helper()
	candidate, err := os.CreateTemp(t.TempDir(), "candidate*")
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

// An unclosed candidate (early error return or panic before Close) is closed
// and unlinked by the cleanup; the earlier error is returned unchanged.
func TestCleanupValidationCandidateClosesUnclosedFile(t *testing.T) {
	for _, prior := range []error{nil, errors.New("prior")} {
		candidate := createCleanupCandidate(t)
		if err := cleanupValidationCandidate("/etc/x", candidate, false, prior); err != prior {
			t.Fatalf("cleanup error = %v, want %v", err, prior)
		}
		if err := candidate.Close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("candidate left open by cleanup: %v", err)
		}
		if _, err := os.Stat(candidate.Name()); !os.IsNotExist(err) {
			t.Fatalf("candidate not removed: %v", err)
		}
	}
}

// A close error from the cleanup is only reported when nothing failed before;
// this is also what hides the second Close after a failed explicit Close.
func TestCleanupValidationCandidateCloseErrorPrecedence(t *testing.T) {
	candidate := createCleanupCandidate(t)
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	err := cleanupValidationCandidate("/etc/x", candidate, false, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "file /etc/x: close validation candidate: ") || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("close error without prior error = %v", err)
	}

	candidate = createCleanupCandidate(t)
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	prior := errors.New("prior")
	if err := cleanupValidationCandidate("/etc/x", candidate, false, prior); err != prior {
		t.Fatalf("close error must not replace prior error, got %v", err)
	}
}

// A successfully closed candidate is not closed again, only unlinked.
func TestCleanupValidationCandidateSkipsCloseWhenClosed(t *testing.T) {
	candidate := createCleanupCandidate(t)
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	wantValidationErr(t, cleanupValidationCandidate("/etc/x", candidate, true, nil), "")
	if _, err := os.Stat(candidate.Name()); !os.IsNotExist(err) {
		t.Fatalf("candidate not removed: %v", err)
	}
}

func TestRemoveValidationCandidateToleratesMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	if err := removeValidationCandidate("/etc/x", missing, nil); err != nil {
		t.Fatalf("missing candidate error = %v", err)
	}
	prior := errors.New("prior")
	if err := removeValidationCandidate("/etc/x", missing, prior); err != prior {
		t.Fatalf("missing candidate must keep prior error, got %v", err)
	}
}

// faultyCandidate wraps the real candidate created by validateCandidate and
// fails (or panics on) the first call of one step; later calls, such as the
// deferred cleanup's Close, reach the real file. A failing Close still closes
// the real file, like a close(2) that reports an error after releasing the
// descriptor.
type faultyCandidate struct {
	*os.File
	failOn  string
	panicOn string
	fired   bool
}

func (c *faultyCandidate) fault(step string) error {
	if c.fired {
		return nil
	}
	if c.panicOn == step {
		c.fired = true
		panic("injected " + step + " panic")
	}
	if c.failOn == step {
		c.fired = true
		return errors.New("injected " + step + " failure")
	}
	return nil
}

func (c *faultyCandidate) Write(p []byte) (int, error) {
	if err := c.fault("write"); err != nil {
		return 0, err
	}
	return c.File.Write(p)
}

func (c *faultyCandidate) Sync() error {
	if err := c.fault("sync"); err != nil {
		return err
	}
	return c.File.Sync()
}

func (c *faultyCandidate) Close() error {
	if err := c.fault("close"); err != nil {
		_ = c.File.Close()
		return err
	}
	return c.File.Close()
}

// injectFaultyCandidate makes validateCandidate stage into a faultyCandidate
// and returns a pointer to the real file it created, for later inspection.
func injectFaultyCandidate(t *testing.T, failOn, panicOn string) **os.File {
	t.Helper()
	var created *os.File
	prev := createValidationCandidate
	t.Cleanup(func() { createValidationCandidate = prev })
	createValidationCandidate = func(dir, pattern string) (validationCandidateFile, error) {
		file, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		created = file
		return &faultyCandidate{File: file, failOn: failOn, panicOn: panicOn}, nil
	}
	return &created
}

// faultyValidateResult is what runFaultyValidateCandidate observed.
type faultyValidateResult struct {
	target       string
	created      *os.File // the real candidate file behind the wrapper
	validatorRan bool
	err          error
	recovered    any
}

// runFaultyValidateCandidate drives validateCandidate itself with a validator
// that records whether it ran, recovering an injected panic.
func runFaultyValidateCandidate(t *testing.T, failOn, panicOn string) faultyValidateResult {
	t.Helper()
	res := faultyValidateResult{target: filepath.Join(privateValidationDir(t), "service.conf")}
	marker := filepath.Join(t.TempDir(), "validator-ran")
	validator := writeValidationScript(t, `touch "`+marker+`"`)
	f := &File{validationBin: validator, validationArgs: []string{CandidatePath}}
	created := injectFaultyCandidate(t, failOn, panicOn)
	func() {
		defer func() { res.recovered = recover() }()
		res.err = f.validateCandidate(res.target, []byte("candidate"))
	}()
	if *created == nil {
		t.Fatal("validateCandidate did not create a candidate")
	}
	res.created = *created
	_, statErr := os.Stat(marker)
	res.validatorRan = statErr == nil
	return res
}

// assertCandidateReleased checks the real candidate's descriptor is closed
// and its file removed, whatever went wrong while staging it.
func assertCandidateReleased(t *testing.T, created *os.File) {
	t.Helper()
	if err := created.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("candidate descriptor leaked: second close = %v", err)
	}
	if _, err := os.Stat(created.Name()); !os.IsNotExist(err) {
		t.Fatalf("candidate %s not removed: %v", created.Name(), err)
	}
}

// Every staging failure aborts before the validator runs, reports the failing
// step and still closes and removes the candidate.
func TestValidateCandidateStagingFailures(t *testing.T) {
	for _, step := range []string{"write", "sync", "close"} {
		t.Run(step, func(t *testing.T) {
			res := runFaultyValidateCandidate(t, step, "")
			if res.recovered != nil {
				t.Fatalf("unexpected panic: %v", res.recovered)
			}
			wantValidationErr(t, res.err, "file "+res.target+": "+step+" validation candidate: injected "+step+" failure")
			if res.validatorRan {
				t.Fatal("validator ran on a candidate that was not fully staged")
			}
			assertCandidateReleased(t, res.created)
		})
	}
}

// A panic while staging propagates, but the deferred cleanup still closes
// and removes the candidate and the validator never runs.
func TestValidateCandidatePanicReleasesCandidate(t *testing.T) {
	for _, step := range []string{"write", "sync", "close"} {
		t.Run(step, func(t *testing.T) {
			res := runFaultyValidateCandidate(t, "", step)
			if res.recovered != "injected "+step+" panic" {
				t.Fatalf("recovered = %v, want injected %s panic", res.recovered, step)
			}
			if res.validatorRan {
				t.Fatal("validator ran after a staging panic")
			}
			assertCandidateReleased(t, res.created)
		})
	}
}

// The injected wrapper passes a clean run through unchanged, so the tests
// above differ from production only in the step they break.
func TestValidateCandidateThroughSeamSucceeds(t *testing.T) {
	res := runFaultyValidateCandidate(t, "", "")
	if res.recovered != nil || res.err != nil || !res.validatorRan {
		t.Fatalf("clean run: err=%v panic=%v validatorRan=%v", res.err, res.recovered, res.validatorRan)
	}
	assertCandidateReleased(t, res.created)
}
