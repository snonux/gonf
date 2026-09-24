package seal

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// This file makes a new signer (task 7g2, docs/design/plan-signing.md "Keys": the
// `gonf plan-signer-keygen` convenience, since unlike age there is no
// external keygen tool for this format). GenerateSigner draws a fresh
// Ed25519 key and WriteSignerFile stores it as a signer file LoadSigner
// reads back, so the CLI never imports crypto/ed25519 or handles the
// secret line itself.
//
// The file is created, never replaced: O_CREAT|O_EXCL|O_NOFOLLOW with mode
// 0600 (a umask can only narrow it, which LoadSigner still accepts), in a
// parent directory reached by the same no-follow walk LoadSigner uses. So
// an existing file (an older key an operator still needs) is never
// overwritten, and a symlink planted at the path or in a directory above it
// never redirects the secret elsewhere.

// ErrSignerFileExists is what WriteSignerFile's refusal to replace an
// existing path (a symlink included) wraps.
var ErrSignerFileExists = errors.New("signer file already exists; refusing to overwrite it")

// signerFileHeader opens every file WriteSignerFile writes; the "#" lines
// are comments LoadSigner ignores.
const signerFileHeader = "# gonf plan signer secret key (docs/design/plan-signing.md). Keep this file private.\n"

// GenerateSigner returns a new Signer with a random Ed25519 key from
// crypto/rand.
func GenerateSigner() (Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Signer{}, fmt.Errorf("plan/seal: generate signer key: %w", err)
	}
	return newSigner(priv), nil
}

// WriteSignerFile creates path as a new signer file holding s, in the
// format LoadSigner reads (a comment naming the public key, then the one
// GONF-SIGNER-SECRET-ED25519 line). It refuses an invalid Signer
// (ErrSignerInvalid) and an existing path, a symlink included
// (ErrSignerFileExists), and never follows
// a symlink in any directory above it (ErrSignerSymlink); a missing parent
// directory wraps os.ErrNotExist (it is not created). A partly written file
// is removed again. No error ever includes key material.
func WriteSignerFile(path string, s Signer) error {
	if !s.valid() {
		return ErrSignerInvalid
	}
	dirFD, name, err := openSignerParent(path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return signerFile.errorf(path, ErrSignerFileExists)
		}
		return signerFile.errorf(path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if err := writeSignerContent(f, s); err != nil {
		_ = f.Close()
		_ = unix.Unlinkat(dirFD, name, 0)
		return signerFile.errorf(path, err)
	}
	return nil
}

// openSignerParent walks to path's parent directory without following a
// symlink (the walk LoadSigner's keyFileKind uses) and returns its
// descriptor and path's final name.
func openSignerParent(path string) (dirFD int, name string, err error) {
	base, parts := safepath.Split(path)
	if len(parts) == 0 {
		return -1, "", fmt.Errorf("plan/seal: %s %s: not a file path", signerFile.label, path)
	}
	dirFD, err = safepath.Walk{}.Open(base, parts[:len(parts)-1])
	if err != nil {
		return -1, "", signerFile.pathError(path, err)
	}
	return dirFD, parts[len(parts)-1], nil
}

// writeSignerContent writes s's signer file content to f, syncs and closes
// it. The buffer holding the secret line is cleared afterwards (best
// effort, as in LoadSigner).
func writeSignerContent(f *os.File, s Signer) error {
	prefix := signerFileHeader + "# public key: " + s.Public().String() + "\n" + signerSecretType + " "
	seed := s.privateKey().Seed() // a copy, cleared below like content
	// Sized up front so no append reallocates and leaves an uncleared copy
	// of the secret line behind.
	content := make([]byte, 0, len(prefix)+keyEncoding.EncodedLen(len(seed))+1)
	content = append(content, prefix...)
	content = keyEncoding.AppendEncode(content, seed)
	content = append(content, '\n')
	defer clear(content)
	defer clear(seed)
	if _, err := f.Write(content); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}
