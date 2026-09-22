package secret

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// These tests pin FileProvider, the provider MustSecret and OptionalSecret
// have always used: exact bytes, the historical error wording (moved here
// from api/secret_traversal_test.go, which called the old api loadSecret
// directly) and, new with the provider contract, the error kind of each
// failure. All values are synthetic and live in t.TempDir.

const neverReport = "never-report-this-secret"

// useWorkDir makes a fresh temp dir the working directory for this test.
func useWorkDir(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

// writeFile writes value to secrets/path, creating directories 0700.
func writeFile(t *testing.T, path, value string) {
	t.Helper()
	path = filepath.Join(DefaultDir, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

// wantFileErr fails unless FileProvider resolves ref to exactly the error
// want of kind kind, without bytes and without leaking neverReport.
func wantFileErr(t *testing.T, ref Ref, kind error, want string) {
	t.Helper()
	data, err := FileProvider{}.Resolve(context.Background(), ref)
	if err == nil || err.Error() != want {
		t.Fatalf("Resolve(%q) = (%q, %v), want error %q", ref, data, err, want)
	}
	if data != nil {
		t.Fatalf("Resolve(%q) returned bytes with an error: %q", ref, data)
	}
	if KindOf(err) != kind || !errors.Is(err, kind) {
		t.Fatalf("Resolve(%q) kind = %v, want %v", ref, KindOf(err), kind)
	}
	if strings.Contains(err.Error(), neverReport) {
		t.Fatalf("error leaked a secret value: %v", err)
	}
}

func TestFileProviderReturnsExactBytes(t *testing.T) {
	useWorkDir(t)
	const value = " leading\ntrailing \x00bytes\n"
	writeFile(t, "var/key", value)
	for _, ref := range []Ref{"var/key", "/var/key", `\var/key`, "var/./key"} {
		data, err := FileProvider{}.Resolve(context.Background(), ref)
		if err != nil || string(data) != value {
			t.Fatalf("Resolve(%q) = (%q, %v), want %q", ref, data, err, value)
		}
	}
	// A backslash that is not leading is an ordinary name character.
	writeFile(t, `var\key`, "bs")
	if data, err := (FileProvider{}).Resolve(context.Background(), `\var\key`); err != nil || string(data) != "bs" {
		t.Fatalf(`Resolve("\\var\\key") = (%q, %v), want the file named var\key`, data, err)
	}
	// A large value spans several read chunks and still comes back intact.
	big := strings.Repeat("0123456789abcdef", readChunk/8)
	writeFile(t, "big", big)
	if data, err := (FileProvider{}).Resolve(context.Background(), "big"); err != nil || string(data) != big {
		t.Fatalf("Resolve(big) = (%d bytes, %v), want %d bytes", len(data), err, len(big))
	}
}

// An empty file is returned as empty bytes: the non-empty rule belongs to
// the api helpers, not to the provider.
func TestFileProviderReturnsEmptyFileAsIs(t *testing.T) {
	useWorkDir(t)
	writeFile(t, "empty", "")
	data, err := FileProvider{}.Resolve(context.Background(), "empty")
	if err != nil || len(data) != 0 {
		t.Fatalf("Resolve(empty) = (%q, %v), want empty bytes", data, err)
	}
}

// A component that is a regular file where a directory is needed has always
// been reported with the symlink wording (the walk sees ENOTDIR for both).
func TestFileProviderRegularFileIntermediateIsReportedAsSymlink(t *testing.T) {
	useWorkDir(t)
	writeFile(t, "file", "x")
	wantFileErr(t, "file/key", ErrInvalid, `secret path "secrets/file/key" contains a symlink`)
}

// Missing below the secrets directory — the file itself or a directory on
// its way — is ErrNotFound, with the message MustSecret has always used.
func TestFileProviderMissingBelowRootIsNotFound(t *testing.T) {
	useWorkDir(t)
	writeFile(t, "a/present", "x")
	wantFileErr(t, "key", ErrNotFound, `secret "key" is missing`)
	wantFileErr(t, "a/missing", ErrNotFound, `secret "a/missing" is missing`)
	wantFileErr(t, "a/missing/key", ErrNotFound, `secret "a/missing/key" is missing`)
}

// A missing secrets directory is the store being unavailable (wrong working
// directory, renamed or unmounted tree), not every secret being absent — an
// optional lookup must not silently drop them all. (Before the provider
// contract it read as "missing".)
func TestFileProviderMissingRootIsUnavailable(t *testing.T) {
	useWorkDir(t)
	for _, ref := range []Ref{"key", "a/b/key"} {
		wantFileErr(t, ref, ErrUnavailable,
			`secret "`+string(ref)+`": secrets directory "secrets" not found in the working directory`)
	}
	// No ENOENT cause: an unavailable store must not match fs.ErrNotExist.
	if _, err := (FileProvider{}).Resolve(context.Background(), "key"); errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing root matches fs.ErrNotExist: %v", err)
	}
	data, err := FileProvider{Dir: "vault"}.Resolve(context.Background(), "key")
	if KindOf(err) != ErrUnavailable || data != nil || !strings.Contains(err.Error(), `"vault"`) {
		t.Fatalf("Resolve with missing custom dir = (%q, %v), want ErrUnavailable naming vault", data, err)
	}
}

// Other open failures keep the bare errno after the secret's name and are
// ErrUnreadable; the errno stays matchable.
func TestFileProviderPermissionDeniedIsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	useWorkDir(t)
	writeFile(t, "locked/key", neverReport)
	locked := filepath.Join(DefaultDir, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	wantFileErr(t, "locked/key", ErrUnreadable, `open secret "locked/key": permission denied`)
	if _, err := (FileProvider{}).Resolve(context.Background(), "locked/key"); !errors.Is(err, unix.EACCES) {
		t.Fatalf("permission error does not wrap EACCES: %v", err)
	}
}

// A secrets directory that cannot be searched is the store failing, not one
// unreadable secret: ErrUnavailable, with the historical wording, for a
// secret directly below it and one further down.
func TestFileProviderUnreadableRootIsUnavailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	useWorkDir(t)
	writeFile(t, "key", neverReport)
	writeFile(t, "sub/key", neverReport)
	if err := os.Chmod(DefaultDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(DefaultDir, 0o700) })
	wantFileErr(t, "key", ErrUnavailable, `open secret "key": permission denied`)
	wantFileErr(t, "sub/key", ErrUnavailable, `open secret "sub/key": permission denied`)
}

// A failure of Dir itself is a store failure even when its errno is ENOENT
// (secrets/ vanishing between open and access check): openError checks the
// root before the not-found case. A symlinked root stays ErrInvalid.
func TestOpenErrorClassifiesRootBeforeNotFound(t *testing.T) {
	err := openError("k", DefaultDir, "secrets/k", rootError{unix.ENOENT})
	if KindOf(err) != ErrUnavailable || err.Error() != `open secret "k": no such file or directory` {
		t.Fatalf("root ENOENT = %v (kind %v), want ErrUnavailable", err, KindOf(err))
	}
	err = openError("k", DefaultDir, "secrets/k", rootError{safepath.ErrSymlink})
	if KindOf(err) != ErrInvalid {
		t.Fatalf("root symlink = %v (kind %v), want ErrInvalid", err, KindOf(err))
	}
}

// Dir removed, renamed or replaced after it was opened and checked, but
// before the lookup below it finished (the window between openRoot and the
// walk), is a store failure: the ENOENT the lookup then sees is re-examined
// by rootVanished and reported as ErrUnavailable, never as not-found. With
// Dir intact, a missing entry stays ErrNotFound. (z52 review 3.)
func TestFileProviderRootVanishingMidLookupIsUnavailable(t *testing.T) {
	const vanished = `secret %q: secrets directory "secrets" was removed or replaced during the lookup`
	for name, tc := range map[string]struct {
		mutate   func() error
		ref      string
		wantKind error
	}{
		"intact, missing entry": {func() error { return nil }, "sub/missing", ErrNotFound},
		"removed":               {func() error { return os.RemoveAll(DefaultDir) }, "sub/key", ErrUnavailable},
		"renamed":               {func() error { return os.Rename(DefaultDir, "moved") }, "sub/missing", ErrUnavailable},
		"replaced": {func() error {
			if err := os.Rename(DefaultDir, "moved"); err != nil {
				return err
			}
			return os.Mkdir(DefaultDir, 0o700)
		}, "missing", ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			useWorkDir(t)
			writeFile(t, "sub/key", neverReport)
			rootFD, err := openRoot(DefaultDir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(rootFD) }()
			if err := tc.mutate(); err != nil {
				t.Fatal(err)
			}
			file, err := openBelowRoot(rootFD, DefaultDir, filepath.FromSlash(tc.ref))
			if file != nil {
				_ = file.Close()
				t.Fatalf("opened %q after the root changed", tc.ref)
			}
			got := openError(Ref(tc.ref), DefaultDir, filepath.Join(DefaultDir, tc.ref), err)
			if KindOf(got) != tc.wantKind {
				t.Fatalf("kind = %v (%v), want %v", KindOf(got), got, tc.wantKind)
			}
			if tc.wantKind == ErrUnavailable && got.Error() != fmt.Sprintf(vanished, tc.ref) {
				t.Fatalf("error = %q, want %q", got, fmt.Sprintf(vanished, tc.ref))
			}
		})
	}
}

// The last component is opened without following a symlink even when the
// link points at a regular file inside secrets/, and a directory or FIFO
// there is not a secret.
func TestFileProviderFinalComponentRules(t *testing.T) {
	useWorkDir(t)
	writeFile(t, "dir/key", "x")
	if err := os.Symlink("key", filepath.Join(DefaultDir, "dir", "alias")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(DefaultDir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantFileErr(t, "dir/alias", ErrInvalid, `secret path "secrets/dir/alias" contains a symlink`)
	wantFileErr(t, "dir", ErrInvalid, `secret "dir" is not a regular file`)
	wantFileErr(t, "fifo", ErrInvalid, `secret "fifo" is not a regular file`)
}

// Symlinks anywhere — the root, an intermediate directory, a dangling
// final link — are refused as ErrInvalid, never followed and never
// reported as not found.
func TestFileProviderRefusesSymlinks(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		useWorkDir(t)
		if err := os.MkdirAll("external", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("external", "key"), []byte(neverReport), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("external", DefaultDir); err != nil {
			t.Fatal(err)
		}
		wantFileErr(t, "key", ErrInvalid, `secret path "secrets/key" contains a symlink`)
	})
	t.Run("intermediate", func(t *testing.T) {
		useWorkDir(t)
		writeFile(t, "actual/key", neverReport)
		if err := os.Symlink("actual", filepath.Join(DefaultDir, "nested")); err != nil {
			t.Fatal(err)
		}
		wantFileErr(t, "nested/key", ErrInvalid, `secret path "secrets/nested/key" contains a symlink`)
	})
	t.Run("dangling", func(t *testing.T) {
		useWorkDir(t)
		if err := os.MkdirAll(DefaultDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../outside-not-yet-created", filepath.Join(DefaultDir, "link")); err != nil {
			t.Fatal(err)
		}
		wantFileErr(t, "link", ErrInvalid, `secret path "secrets/link" contains a symlink`)
	})
}

// Negative: references that are empty or leave the directory are refused
// before anything is opened, as ErrInvalid; so is a misconfigured Dir, as
// ErrUnavailable.
func TestFileProviderRefusesInvalidReferences(t *testing.T) {
	useWorkDir(t)
	if err := os.WriteFile("outside", []byte(neverReport), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, "key", "x")
	wantFileErr(t, "", ErrInvalid, "secret path must not be empty")
	wantFileErr(t, "///", ErrInvalid, "secret path must not be empty")
	wantFileErr(t, "nested/../../outside", ErrInvalid, `invalid secret path "nested/../../outside"`)
	wantFileErr(t, "..", ErrInvalid, `invalid secret path ".."`)
	wantFileErr(t, ".", ErrInvalid, `invalid secret path "."`)
	_, err := FileProvider{Dir: "a/b"}.Resolve(context.Background(), "key")
	if KindOf(err) != ErrUnavailable {
		t.Fatalf("multi-component Dir: err = %v, want ErrUnavailable", err)
	}
	// A backslash is an ordinary name character on unix, so `a\b` is one
	// component (the safepath rule) and a valid directory (z52 review 3).
	if err := os.MkdirAll(`a\b`, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(`a\b/key`, []byte("bs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := (FileProvider{Dir: `a\b`}).Resolve(context.Background(), "key"); err != nil || string(data) != "bs" {
		t.Fatalf(`FileProvider{Dir: "a\\b"} = (%q, %v), want "bs"`, data, err)
	}
	if _, err := (FileProvider{Dir: ".."}).Resolve(context.Background(), "key"); KindOf(err) != ErrUnavailable ||
		err.Error() != `secret "key": file provider directory must not be ".."` {
		t.Fatalf(`FileProvider{Dir: ".."} = %v, want the ErrUnavailable refusal`, err)
	}
}

// flakyCtx is a context whose Err turns to Canceled after okCalls calls: it
// cancels deterministically in the middle of a FileProvider read.
type flakyCtx struct {
	context.Context
	okCalls int
}

func (c *flakyCtx) Err() error {
	if c.okCalls > 0 {
		c.okCalls--
		return nil
	}
	return context.Canceled
}

func TestFileProviderHonoursCancellation(t *testing.T) {
	useWorkDir(t)
	writeFile(t, "big", strings.Repeat("x", 3*readChunk))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := FileProvider{}.Resolve(ctx, "big")
	if !errors.Is(err, context.Canceled) || data != nil || KindOf(err) != nil {
		t.Fatalf("pre-cancelled Resolve = (%d bytes, %v), want context.Canceled only", len(data), err)
	}

	// A done ctx is refused before the reference is checked or anything is
	// opened: an invalid reference, a missing file and a missing root all
	// report the cancellation, not what opening them would have found.
	for _, ref := range []Ref{"", "missing"} {
		if _, err := (FileProvider{}).Resolve(ctx, ref); !errors.Is(err, context.Canceled) || KindOf(err) != nil {
			t.Fatalf("done ctx, ref %q: err = %v, want context.Canceled before any open", ref, err)
		}
	}
	if _, err := (FileProvider{Dir: "absent"}).Resolve(ctx, "key"); !errors.Is(err, context.Canceled) || KindOf(err) != nil {
		t.Fatalf("done ctx, missing root: err = %v, want context.Canceled before any open", err)
	}

	// Cancelled in the middle of the read: the entry check and readAll's
	// first loop check consume the two ok calls, so the third check lands
	// after the first chunk was read and the partially-read buffer is
	// dropped.
	mid := &flakyCtx{Context: context.Background(), okCalls: 2}
	data, err = FileProvider{}.Resolve(mid, "big")
	if !errors.Is(err, context.Canceled) || data != nil || IsNotFound(err) {
		t.Fatalf("mid-read cancel = (%d bytes, %v), want context.Canceled", len(data), err)
	}
}
