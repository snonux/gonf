package seal

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// writeIdentityFile writes an identity file at dir/name containing line
// (plus a trailing newline), with the given permission bits, and returns
// its full path.
func writeIdentityFile(t *testing.T, dir, name, line string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(line+"\n"), perm); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	// os.WriteFile's mode is narrowed by umask on creation; force it exactly,
	// since these tests are about the mode LoadIdentities sees.
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod identity file: %v", err)
	}
	return path
}

func TestLoadIdentitiesAcceptsHybridOnly(t *testing.T) {
	_, identity := genKeyPair(t)
	dir := t.TempDir()
	path := writeIdentityFile(t, dir, "identity", identity.inner.String(), 0o600)

	loaded, err := LoadIdentities(path)
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("got %d identities, want 1", len(loaded))
	}

	// Prove it is usable: seal to the matching recipient and open with the
	// loaded identity.
	recipient := Recipient{inner: identity.inner.Recipient()}
	sealed := sealBytes(t, "loaded identity works", []Recipient{recipient})
	r, err := Open(bytes.NewReader(sealed), loaded)
	if err != nil {
		t.Fatalf("Open with loaded identity: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "loaded identity works" {
		t.Fatalf("got %q", got)
	}
}

func TestLoadIdentitiesIgnoresCommentsAndBlankLines(t *testing.T) {
	_, identity := genKeyPair(t)
	dir := t.TempDir()
	content := "# a comment\n\n" + identity.inner.String() + "\n"
	path := filepath.Join(dir, "identity")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := LoadIdentities(path)
	if err != nil {
		t.Fatalf("LoadIdentities: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("got %d identities, want 1", len(loaded))
	}
}

func TestLoadIdentitiesRefusesClassicX25519(t *testing.T) {
	dir := t.TempDir()
	// A real classic identity line; never echoed back on refusal.
	classicID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate X25519 identity: %v", err)
	}
	classicLine := classicID.String()
	path := writeIdentityFile(t, dir, "identity", classicLine, 0o600)

	_, err = LoadIdentities(path)
	if !errors.Is(err, ErrIdentityRefused) {
		t.Fatalf("got %v, want ErrIdentityRefused", err)
	}
	if strings.Contains(err.Error(), classicLine) {
		t.Fatalf("error echoes the classic identity's content: %v", err)
	}
}

func TestLoadIdentitiesRefusesGroupReadableFile(t *testing.T) {
	_, identity := genKeyPair(t)
	dir := t.TempDir()
	path := writeIdentityFile(t, dir, "identity", identity.inner.String(), 0o640)

	_, err := LoadIdentities(path)
	if !errors.Is(err, ErrIdentityMode) {
		t.Fatalf("got %v, want ErrIdentityMode", err)
	}
}

func TestLoadIdentitiesRefusesOtherReadableFile(t *testing.T) {
	_, identity := genKeyPair(t)
	dir := t.TempDir()
	path := writeIdentityFile(t, dir, "identity", identity.inner.String(), 0o604)

	_, err := LoadIdentities(path)
	if !errors.Is(err, ErrIdentityMode) {
		t.Fatalf("got %v, want ErrIdentityMode", err)
	}
}

func TestLoadIdentitiesRefusesSymlink(t *testing.T) {
	_, identity := genKeyPair(t)
	dir := t.TempDir()
	realPath := writeIdentityFile(t, dir, "identity-real", identity.inner.String(), 0o600)
	linkPath := filepath.Join(dir, "identity-link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := LoadIdentities(linkPath)
	if !errors.Is(err, ErrIdentitySymlink) {
		t.Fatalf("got %v, want ErrIdentitySymlink", err)
	}
}

func TestLoadIdentitiesRefusesNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "identity")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := LoadIdentities(sub)
	if !errors.Is(err, ErrIdentityNotRegular) {
		t.Fatalf("got %v, want ErrIdentityNotRegular", err)
	}
}

func TestLoadIdentitiesMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadIdentities(filepath.Join(dir, "missing"))
	if err == nil {
		t.Fatal("LoadIdentities of a missing path succeeded")
	}
}

func TestLoadIdentitiesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeIdentityFile(t, dir, "identity", "# only a comment", 0o600)
	_, err := LoadIdentities(path)
	if err == nil {
		t.Fatal("LoadIdentities of a comment-only file succeeded")
	}
}

// TestCheckOwnerAndModeRefusesForeignOwner exercises the ownership rule
// directly with a fabricated safepath.Info, the technique
// internal/remote/crossbuild_identity_test.go uses for the same reason: a
// test cannot chown a real file to a uid it does not own without being
// root, but the policy function itself only needs a value, not a real
// syscall, to check.
func TestCheckOwnerAndModeRefusesForeignOwner(t *testing.T) {
	me := unix.Geteuid()
	foreign := me + 1
	info := safepath.Info{Mode: unix.S_IFREG | 0o600, UID: uint32(foreign)}

	err := identityFile.checkOwnerAndMode(info, "/fake/identity", me)
	if !errors.Is(err, ErrIdentityNotOwned) {
		t.Fatalf("got %v, want ErrIdentityNotOwned", err)
	}
}

func TestCheckOwnerAndModeAcceptsOwnFileWithStrictMode(t *testing.T) {
	me := unix.Geteuid()
	info := safepath.Info{Mode: unix.S_IFREG | 0o600, UID: uint32(me)}
	if err := identityFile.checkOwnerAndMode(info, "/fake/identity", me); err != nil {
		t.Fatalf("identityFile.checkOwnerAndMode rejected an own 0600 file: %v", err)
	}
}
