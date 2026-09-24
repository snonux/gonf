package seal

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Corruption-only tamper tests.
//
// These tests prove that Open notices a sealed artifact was damaged after
// sealing (truncated, bit-flipped, or header-damaged) — never that Open
// verifies WHO sealed it. age has no sender authentication: anyone who
// knows a recipient's public key (which is everyone, since recipients are
// meant to be shared) can produce a perfectly valid, uncorrupted plan.age.
// That is a provenance property this package does not have and does not
// claim; see the package doc's "Confidentiality only, never provenance" and
// docs/design/plan-encryption.md "Provenance". Do not read a passing test here as
// evidence of authenticity — it only proves age's AEAD notices when bytes
// change after sealing.

// tamperPlaintext is long enough (well over one 64 KiB stream segment) that
// truncation and a bit flip land unambiguously in different segments of the
// payload, not both in the same final chunk.
var tamperPlaintext = "GONF-PUSH/1\nblobs 0\nplan\n" +
	// Repeat a recognizable, non-degenerate pattern rather than one byte
	// value, so a test that (incorrectly) got usable plaintext back would
	// be easy to notice by eye in a failure message, not just by length.
	strings.Repeat("gonf-plan-seal-tamper-test-payload-0123456789-", 3000)

// openAndDrain calls Open and, if it succeeds, reads the result to EOF,
// recovering a panic into a clean test failure: neither is an acceptable
// outcome for tampered input, only a returned error is.
func openAndDrain(t *testing.T, sealed []byte, identities []Identity) (plaintext []byte, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Open/read panicked on tampered input instead of returning an error: %v", r)
		}
	}()
	r, openErr := Open(bytes.NewReader(sealed), identities)
	if openErr != nil {
		return nil, openErr
	}
	plaintext, err = io.ReadAll(r)
	return plaintext, err
}

func TestOpenDetectsTruncation(t *testing.T) {
	recipient, identity := genKeyPair(t)
	sealed := sealBytes(t, tamperPlaintext, []Recipient{recipient})

	// Drop the last 10% of the file: guaranteed to cut into the payload's
	// final authenticated chunk, well past the header.
	cut := sealed[:len(sealed)-len(sealed)/10]

	plaintext, err := openAndDrain(t, cut, []Identity{identity})
	if err == nil {
		t.Fatalf("Open accepted a truncated artifact and returned %d bytes of plaintext without error", len(plaintext))
	}
	if bytes.Contains(plaintext, []byte(tamperPlaintext)) {
		t.Fatalf("truncated artifact yielded the full original plaintext despite an error")
	}
}

func TestOpenDetectsBitFlip(t *testing.T) {
	recipient, identity := genKeyPair(t)
	sealed := sealBytes(t, tamperPlaintext, []Recipient{recipient})

	// Flip a single bit near the end of the file: unambiguously inside the
	// payload's ciphertext (past any plausible header size), so this
	// exercises the stream AEAD, not header parsing (see
	// TestOpenDetectsHeaderDamage for that).
	tampered := append([]byte(nil), sealed...)
	last := len(tampered) - 1
	tampered[last] ^= 0x01

	plaintext, err := openAndDrain(t, tampered, []Identity{identity})
	if err == nil {
		t.Fatalf("Open accepted a single-bit-flipped artifact and returned %d bytes without error", len(plaintext))
	}
	if bytes.Equal(plaintext, []byte(tamperPlaintext)) {
		t.Fatalf("bit-flipped artifact yielded the exact original plaintext despite an error")
	}
}

func TestOpenDetectsHeaderDamage(t *testing.T) {
	recipient, identity := genKeyPair(t)
	sealed := sealBytes(t, tamperPlaintext, []Recipient{recipient})

	// The age header starts "age-encryption.org/v1\n"; flip a byte inside
	// that magic line itself, guaranteed to be header, not payload.
	if !bytes.HasPrefix(sealed, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("sealed artifact does not start with the expected age magic; test assumption broke")
	}
	tampered := append([]byte(nil), sealed...)
	tampered[5] ^= 0x01 // inside "age-encryption.org/v1"

	plaintext, err := openAndDrain(t, tampered, []Identity{identity})
	if err == nil {
		t.Fatalf("Open accepted a header-damaged artifact and returned %d bytes without error", len(plaintext))
	}
	if bytes.Equal(plaintext, []byte(tamperPlaintext)) {
		t.Fatalf("header-damaged artifact yielded the exact original plaintext despite an error")
	}
}

// TestOpenDetectsHeaderMACDamage corrupts the header's own MAC line (which
// binds the recipient stanza list) rather than the leading magic, to prove
// header-level tamper detection also catches damage that leaves the magic
// and stanzas well-formed.
func TestOpenDetectsHeaderMACDamage(t *testing.T) {
	recipient, identity := genKeyPair(t)
	sealed := sealBytes(t, tamperPlaintext, []Recipient{recipient})

	macIdx := bytes.Index(sealed, []byte("\n---"))
	if macIdx < 0 {
		t.Fatalf("sealed artifact has no \"---\" header/MAC separator; test assumption broke")
	}
	tampered := append([]byte(nil), sealed...)
	// Flip a byte a few positions after the separator line, inside the
	// base64 MAC value itself.
	target := macIdx + 6
	if target >= len(tampered) {
		t.Fatalf("sealed artifact too short to reach the MAC value; test assumption broke")
	}
	tampered[target] ^= 0x01

	plaintext, err := openAndDrain(t, tampered, []Identity{identity})
	if err == nil {
		t.Fatalf("Open accepted a MAC-damaged artifact and returned %d bytes without error", len(plaintext))
	}
	if bytes.Equal(plaintext, []byte(tamperPlaintext)) {
		t.Fatalf("MAC-damaged artifact yielded the exact original plaintext despite an error")
	}
}
