package seal

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"filippo.io/age"
)

// ErrIdentityLineFormat marks an ephemeral identity line ParseEphemeral
// refuses before even classifying it: empty, or containing whitespace or a
// control character (a newline, a carriage return, a tab, a space...). The
// encoded form travels as ONE line of a line-oriented stdin frame, so
// anything that could split or pad that line is refused rather than
// trimmed: the framing layer removes its own line terminator, and a value
// that still carries one here means the framing was corrupted or the value
// tampered with.
var ErrIdentityLineFormat = errors.New("ephemeral identity line is empty or contains whitespace or control characters")

// ErrEphemeralZeroIdentity is returned by EncodeEphemeral for the zero
// Identity, which holds no key to encode.
var ErrEphemeralZeroIdentity = errors.New("plan/seal: zero identity cannot be encoded")

// GenerateEphemeral generates a fresh age1pq hybrid (ML-KEM-768 + X25519)
// key pair ENTIRELY IN MEMORY and returns it as this package's own Identity
// and Recipient. It is the primitive for a per-push, single-use key (see
// docs/design/plan-encryption.md, "Phase 4 design: sealed multi-chunk sticky-dir
// blobs", "Key lifecycle"): the recipient seals data on the controller and
// the identity, delivered to the elevated child over stdin, opens it there.
//
// Nothing here, nor in EncodeEphemeral/ParseEphemeral, touches the file
// system: the key comes from crypto/rand via age.GenerateHybridIdentity and
// stays in the returned values. This file deliberately imports no os, io/fs
// or path packages, and a test (TestEphemeralFileImportsNoFilesystem) pins
// that. It never goes through LoadIdentities, whose ownership, no-follow and
// permission-bit checks belong to long-lived, file-backed operator and
// destination identities, a different threat model from a value handed
// over a pipe.
//
// The Identity is private key material: it must never be logged, written
// to disk or included in an error. It has no String method precisely so
// that a stray %v or %s cannot print it; the only way to obtain its text is
// the explicit EncodeEphemeral.
func GenerateEphemeral() (Identity, Recipient, error) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		// age's error carries only the entropy-source failure text; no key
		// material exists yet at this point, so wrapping it is safe.
		return Identity{}, Recipient{}, fmt.Errorf("plan/seal: generate ephemeral identity: %w", err)
	}
	return Identity{inner: id}, Recipient{inner: id.Recipient()}, nil
}

// EncodeEphemeral returns id as a single line of text, "AGE-SECRET-KEY-PQ-1"
// followed by upper-case Bech32 characters: the same encoding an age
// identity file holds, so it is stable across age versions and
// round-trips through ParseEphemeral. The result contains only
// [A-Z0-9-], never whitespace or a newline, so it can be framed as one line
// on stdin; the caller adds the line terminator.
//
// The result is private key material: never log it, write it to disk or
// place it in an error. The zero Identity yields ErrEphemeralZeroIdentity.
func EncodeEphemeral(id Identity) (string, error) {
	if id.inner == nil {
		return "", ErrEphemeralZeroIdentity
	}
	return id.inner.String(), nil
}

// ParseEphemeral parses line, one line EncodeEphemeral produced (without
// its line terminator), back into an Identity. It is the counterpart for a
// value received over stdin, and is intentionally strict: no trimming, no
// comments, no multiple identities (contrast the file-backed
// LoadIdentities), and no file checks, which make no sense for a pipe.
//
// Refusals never include line or any part of it, since it is private key
// material:
//   - ErrIdentityLineFormat: empty, or contains whitespace/control
//     characters (including an embedded or trailing newline);
//   - ErrIdentityRefused: not an age1pq identity (classic X25519 or
//     unrecognized), by the same policy as LoadIdentities;
//   - ErrIdentityMalformed: age1pq-prefixed but truncated, corrupted or
//     tampered (the Bech32 checksum and key validation fail).
func ParseEphemeral(line string) (Identity, error) {
	if line == "" || strings.IndexFunc(line, isSpaceOrControl) >= 0 {
		return Identity{}, ErrIdentityLineFormat
	}
	id, err := parseIdentityLine(line)
	if err != nil {
		return Identity{}, err
	}
	return Identity{inner: id}, nil
}

// isSpaceOrControl reports whether r is whitespace or a control character,
// covering ASCII and the Unicode spaces (e.g. NBSP, U+2028) that could
// otherwise slip into an encoded value unnoticed.
func isSpaceOrControl(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}
