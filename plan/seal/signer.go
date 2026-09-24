package seal

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode"
	"unicode/utf8"
)

// This file is the key half of plan signing (docs/plan-signing.md, "Keys",
// task 6g2): the Ed25519 signer identity that signs a sealed plan (Signer,
// LoadSigner) and the destination's own list of public keys it will accept
// a signature from (TrustedSigner, LoadTrustedSigners). The envelope itself
// is sign.go.
//
// # File formats
//
// Both files are line-based, blank lines and "#" comments ignored, and
// every key line starts with an explicit type word, so a key of another
// kind pasted into the wrong file (an age identity or recipient, an
// ssh-ed25519 key, a signer's public key in the signer file) is refused by
// class instead of being misread as Ed25519 bytes:
//
//	signer file:          GONF-SIGNER-SECRET-ED25519 <base64 32-byte seed>
//	trusted-signers file: gonf-signer-ed25519 <base64 32-byte public key> [label...]
//
// base64 is the standard alphabet without padding (the encoding age uses
// for its own header lines), decoded strictly, so each key has exactly one
// accepted spelling. The case convention follows age's (upper case for the
// secret, lower case for the public form), and TrustedSigner.String prints
// exactly the trusted-signers line, so an operator copies a signer's public
// line into a destination's file verbatim.
//
// # Hardening
//
// Both loaders open their file with the same keyFileKind policy
// (keyfile.go) the identity and recipients files use: a no-follow walk, a
// regular file owned by the effective uid, and a mode check. The signer
// file is private key material and gets the identity file's 0o077 rule;
// the trusted-signers file holds only public keys and gets the recipients
// file's narrower 0o022 (group/other WRITE) rule, since whoever can write
// it, or plant a symlink at its path, chooses which signatures a
// destination accepts (docs/plan-signing.md threat S4). No error from
// either loader ever includes a byte of the file's content.

// signerSecretType and signerPublicType are the type words that open a
// signer-file line and a trusted-signers line (see "File formats").
const (
	signerSecretType = "GONF-SIGNER-SECRET-ED25519"
	signerPublicType = "gonf-signer-ed25519"
)

// maxKeyFileBytes bounds how much of a signer or trusted-signers file is
// ever read. A real file is well under 1 KiB per key; the bound only stops
// a pathological (or hostile, e.g. a device node would already be refused
// as non-regular) file from being read into memory whole.
const maxKeyFileBytes = 64 << 10

// keyEncoding is the one accepted base64 spelling of a key or signature
// (see "File formats"); Strict refuses non-zero trailing bits, so no two
// spellings decode to the same bytes.
var keyEncoding = base64.RawStdEncoding.Strict()

// Signer-file refusal classes.
var (
	ErrSignerNotRegular = errors.New("signer file is not a regular file")
	ErrSignerSymlink    = errors.New("signer file path contains a symlink")
	ErrSignerNotOwned   = errors.New("signer file is not owned by the current user")
	ErrSignerMode       = errors.New("signer file is readable or writable by group or other")
	ErrSignerRefused    = errors.New("signer key refused: expected a " + signerSecretType + " line")
	ErrSignerMalformed  = errors.New("malformed " + signerSecretType + " line")
	ErrSignerCount      = errors.New("signer file must hold exactly one signing key")
)

// Trusted-signers-file refusal classes.
var (
	ErrTrustedSignersNotRegular = errors.New("trusted-signers file is not a regular file")
	ErrTrustedSignersSymlink    = errors.New("trusted-signers file path contains a symlink")
	ErrTrustedSignersNotOwned   = errors.New("trusted-signers file is not owned by the current user")
	ErrTrustedSignersWritable   = errors.New("trusted-signers file is writable by group or other")
	ErrTrustedSignerRefused     = errors.New("trusted signer refused: expected a " + signerPublicType + " line")
	ErrTrustedSignerMalformed   = errors.New("malformed " + signerPublicType + " line")
)

// ErrKeyFileTooLarge marks a signer or trusted-signers file larger than
// maxKeyFileBytes.
var ErrKeyFileTooLarge = errors.New("key file too large")

// signerFile is the signer file's hardened-open policy: private key
// material, so the identity file's rule (no group or other bit at all).
var signerFile = keyFileKind{
	label:         "signer file",
	errSymlink:    ErrSignerSymlink,
	errNotRegular: ErrSignerNotRegular,
	errNotOwned:   ErrSignerNotOwned,
	errMode:       ErrSignerMode,
	modeMask:      0o077,
	missing:       os.ErrNotExist,
}

// trustedSignersFile is the trusted-signers file's hardened-open policy:
// public keys, so the recipients file's rule (group/other write refused).
var trustedSignersFile = keyFileKind{
	label:         "trusted-signers file",
	errSymlink:    ErrTrustedSignersSymlink,
	errNotRegular: ErrTrustedSignersNotRegular,
	errNotOwned:   ErrTrustedSignersNotOwned,
	errMode:       ErrTrustedSignersWritable,
	modeMask:      0o022,
	missing:       os.ErrNotExist,
}

// Signer is a validated Ed25519 signing identity, as returned by LoadSigner
// and accepted by Sign. The zero value is not valid (Sign refuses it).
//
// Signer implements fmt.Formatter so that no verb (%v, %+v, %#v, %s, %x,
// ...) ever prints the private key: a Signer that ends up in a log line or
// an error message shows only its public key.
type Signer struct {
	key ed25519.PrivateKey
}

// valid reports whether s holds a complete Ed25519 private key.
func (s Signer) valid() bool { return len(s.key) == ed25519.PrivateKeySize }

// Public returns s's public key as a TrustedSigner with no label: the
// entry a destination adds to its trusted-signers file (String prints the
// exact line) to accept plans s signs. It returns the zero TrustedSigner
// for an invalid Signer.
func (s Signer) Public() TrustedSigner {
	if !s.valid() {
		return TrustedSigner{}
	}
	pub, _ := s.key.Public().(ed25519.PublicKey)
	return TrustedSigner{Key: bytes.Clone(pub)}
}

// Format prints only the public key, never the private key, whatever the
// verb (see the Signer doc comment).
func (s Signer) Format(f fmt.State, _ rune) {
	if !s.valid() {
		_, _ = io.WriteString(f, "seal.Signer(invalid)")
		return
	}
	_, _ = io.WriteString(f, "seal.Signer("+s.Public().String()+")")
}

// TrustedSigner is one entry of a destination's trusted-signers file: an
// Ed25519 public key plus the label its line carried. Label is
// documentary only, for an operator reading a log: Verify matches by Key
// bytes alone, never by Label.
type TrustedSigner struct {
	Key   ed25519.PublicKey
	Label string
}

// String returns t as a trusted-signers file line: the type word, the
// base64 key and, when set, the label. It is safe to print (public key).
func (t TrustedSigner) String() string {
	line := signerPublicType + " " + keyEncoding.EncodeToString(t.Key)
	if t.Label != "" {
		line += " " + t.Label
	}
	return line
}

// LoadSigner reads path as a signer file (see "File formats"): exactly one
// GONF-SIGNER-SECRET-ED25519 line. path must pass the signer file's
// hardening (see "Hardening": ErrSignerSymlink, ErrSignerNotRegular,
// ErrSignerNotOwned, ErrSignerMode); a missing file wraps os.ErrNotExist.
// A parse refusal names the line number and class (ErrSignerRefused,
// ErrSignerMalformed, ErrSignerCount, ErrKeyFileTooLarge), never content.
func LoadSigner(path string) (Signer, error) {
	data, err := readKeyFile(signerFile, path)
	if err != nil {
		return Signer{}, err
	}
	// Best effort: do not leave the seed's text form in this buffer for
	// longer than parsing needs it (parsing works on data in place, never
	// on a string copy; Go gives no stronger guarantee than this).
	defer clear(data)
	signer, err := parseSignerFile(data)
	if err != nil {
		return Signer{}, signerFile.errorf(path, err)
	}
	return signer, nil
}

// parseSignerFile parses a signer file's content (see LoadSigner).
func parseSignerFile(data []byte) (Signer, error) {
	var signer Signer
	for n, line := range keyFileLines(data) {
		if len(line) == 0 {
			continue
		}
		if signer.valid() {
			return Signer{}, fmt.Errorf("line %d: %w", n, ErrSignerCount)
		}
		key, err := parseSignerLine(line)
		if err != nil {
			return Signer{}, fmt.Errorf("line %d: %w", n, err)
		}
		signer = Signer{key: key}
	}
	if !signer.valid() {
		return Signer{}, ErrSignerCount
	}
	return signer, nil
}

// parseSignerLine validates one non-blank, non-comment signer-file line,
// never including line (private key material) in an error.
func parseSignerLine(line []byte) (ed25519.PrivateKey, error) {
	fields := bytes.Fields(line)
	if string(fields[0]) != signerSecretType {
		return nil, ErrSignerRefused
	}
	if len(fields) != 2 {
		return nil, ErrSignerMalformed
	}
	seed, err := decodeKey(fields[1], ed25519.SeedSize)
	if err != nil {
		return nil, ErrSignerMalformed
	}
	defer clear(seed)
	return ed25519.NewKeyFromSeed(seed), nil
}

// LoadTrustedSigners reads path as a trusted-signers file (see "File
// formats"): one gonf-signer-ed25519 line per trusted key, at least one.
// path must pass the trusted-signers file's hardening (see "Hardening":
// ErrTrustedSignersSymlink, ErrTrustedSignersNotRegular,
// ErrTrustedSignersNotOwned, ErrTrustedSignersWritable); a missing file
// wraps os.ErrNotExist. A file with no key line is refused with
// ErrNoTrustedSigners rather than returning an empty set, so a caller that
// forgets to check the length still fails closed (Verify refuses an empty
// set too). A parse refusal names the line number and class.
func LoadTrustedSigners(path string) ([]TrustedSigner, error) {
	data, err := readKeyFile(trustedSignersFile, path)
	if err != nil {
		return nil, err
	}
	var out []TrustedSigner
	for n, line := range keyFileLines(data) {
		if len(line) == 0 {
			continue
		}
		ts, err := parseTrustedSignerLine(line)
		if err != nil {
			return nil, trustedSignersFile.errorf(path, fmt.Errorf("line %d: %w", n, err))
		}
		out = append(out, ts)
	}
	if len(out) == 0 {
		return nil, trustedSignersFile.errorf(path, ErrNoTrustedSigners)
	}
	return out, nil
}

// parseTrustedSignerLine validates one non-blank, non-comment
// trusted-signers line: the type word, the key, then an optional label
// (every remaining field, joined by single spaces). A label is printed in
// logs later, so one that is not valid UTF-8 or holds a non-printable
// character (a terminal escape, say) is refused rather than passed on.
func parseTrustedSignerLine(line []byte) (TrustedSigner, error) {
	fields := bytes.Fields(line)
	if string(fields[0]) != signerPublicType {
		return TrustedSigner{}, ErrTrustedSignerRefused
	}
	if len(fields) < 2 {
		return TrustedSigner{}, ErrTrustedSignerMalformed
	}
	key, err := decodeKey(fields[1], ed25519.PublicKeySize)
	if err != nil {
		return TrustedSigner{}, ErrTrustedSignerMalformed
	}
	label := string(bytes.Join(fields[2:], []byte(" ")))
	if !printableLabel(label) {
		return TrustedSigner{}, ErrTrustedSignerMalformed
	}
	return TrustedSigner{Key: key, Label: label}, nil
}

// printableLabel reports whether label is valid UTF-8 made only of
// printable characters (spaces included).
func printableLabel(label string) bool {
	if !utf8.ValidString(label) {
		return false
	}
	for _, r := range label {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// decodeKey decodes src with keyEncoding and requires exactly size bytes.
// It works on []byte rather than string so that a secret's text form is
// never copied into an immutable string the caller could not clear. Its
// error never includes src.
func decodeKey(src []byte, size int) ([]byte, error) {
	if len(src) != keyEncoding.EncodedLen(size) {
		return nil, errors.New("wrong key length")
	}
	b := make([]byte, keyEncoding.DecodedLen(len(src)))
	n, err := keyEncoding.Decode(b, src)
	if err != nil || n != size {
		clear(b)
		return nil, errors.New("bad key encoding")
	}
	return b[:n], nil
}

// readKeyFile opens path with kind's hardened policy and reads at most
// maxKeyFileBytes of it (ErrKeyFileTooLarge beyond that).
func readKeyFile(kind keyFileKind, path string) ([]byte, error) {
	f, err := kind.openChecked(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFileBytes+1))
	if err != nil {
		clear(data)
		return nil, kind.errorf(path, err)
	}
	if len(data) > maxKeyFileBytes {
		clear(data)
		return nil, kind.errorf(path, ErrKeyFileTooLarge)
	}
	return data, nil
}

// keyFileLines yields data's lines numbered from 1, with surrounding white
// space trimmed and "#" comment lines reported as empty so callers skip
// them like blank lines while line numbers stay those an editor shows. The
// yielded slices alias data (no copy of a secret is made).
func keyFileLines(data []byte) func(yield func(int, []byte) bool) {
	return func(yield func(int, []byte) bool) {
		for i, line := range bytes.Split(data, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("#")) {
				line = nil
			}
			if !yield(i+1, line) {
				return
			}
		}
	}
}
