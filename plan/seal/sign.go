package seal

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"time"
)

// This file is the envelope half of plan signing (docs/plan-signing.md,
// "Recommended design", task 6g2, phase signing-1): Sign wraps the bytes
// Seal produced in a GONF-SIGNED-PLAN/1 envelope carrying an Ed25519
// signature over them and the time they were signed, and Verify checks that
// signature against a destination's trusted signers BEFORE anything is
// decrypted, handing the untouched sealed bytes back for Open. Signing is
// encrypt-then-sign: it never looks inside the ciphertext, and Verify
// performs no decryption.
//
// # Envelope (GONF-SIGNED-PLAN/1)
//
//	GONF-SIGNED-PLAN/1\n
//	<signer public key: 32 bytes, 43 base64 characters>\n
//	<signature: 64 bytes, 86 base64 characters>\n
//	signed-at <YYYY-MM-DDTHH:MM:SSZ>\n
//	<the complete plan.age bytes, unchanged, to EOF>
//
// base64 is keyEncoding (signer.go): standard alphabet, no padding,
// decoded strictly, and each line must have its exact length, so every
// envelope has exactly one accepted spelling. The signed-at line
// (signedat.go, task 7g2) is RFC 3339 in UTC at second precision with a
// literal "Z", 20 characters, also with exactly one accepted spelling. The
// payload must itself start with age's own header line
// ("age-encryption.org/v1\n"): Sign refuses to sign anything else and
// Verify refuses to return anything else, so a signed PLAINTEXT plan (or a
// nested envelope) can never come out of a successful Verify and be
// mistaken for a sealed one.
//
// # What the signature covers
//
// The signed message is the magic line followed by every byte after the
// signature line: "GONF-SIGNED-PLAN/1\n" || "signed-at <time>\n" ||
// plan.age. It therefore covers the signing time (so a replayed envelope
// cannot be given a fresher one), every byte of the age header, every
// recipient stanza and the whole ciphertext (docs/plan-signing.md threat
// S5: a replaced or edited ciphertext invalidates it), and the envelope
// version too, so a signature cannot be relabelled under another envelope
// version or format that signs the bare payload. Ed25519 itself binds the
// public key into the signature, so the key line needs no separate
// coverage. There is no algorithm field to confuse: the magic fixes the
// algorithm (pure Ed25519, RFC 8032), and a future algorithm or field
// change gets a new magic.
//
// # Freshness: carried here, enforced by the caller
//
// The signed-at time was added to /1 by task 7g2, before any gonf binary
// produced a /1 envelope, as docs/plan-signing.md "Replay and rollback"
// requires. (v0.17.0's library-only Sign wrote the undated layout; Verify
// refuses such an envelope as malformed, since its fourth line is the age
// header, and its signature covers another message.) Verify authenticates
// it and returns it (Verified.SignedAt) but enforces no freshness window:
// that policy, against the destination's own clock, is the caller's (phase
// signing-3, task 8g2: gonf apply and gonf plan-verify check it in
// internal/cli/apply_signed.go, only after Verify succeeded). A Verify success on
// its own says only which trusted key produced these exact bytes and when
// that key's holder claims to have signed them.

// SignedPlanMagic is the envelope's first line, without its "\n". A
// caller sniffing an input's first line uses it to decide whether to call
// Verify at all (docs/plan-signing.md "Verification order").
const SignedPlanMagic = "GONF-SIGNED-PLAN/1"

// ageHeaderLine is how every age v1 stream, and so every Seal output,
// begins.
const ageHeaderLine = "age-encryption.org/v1\n"

// signedPlanFamily is the version-independent prefix of SignedPlanMagic:
// an input starting with it but not with the full magic line is a
// malformed or unsupported envelope, not an unsigned input.
const signedPlanFamily = "GONF-SIGNED-PLAN/"

// SignedSniffLen is how many leading bytes LooksSigned needs to decide.
// A caller peeking a stream (gonf apply -, task 8g2) peeks at least this
// many bytes before choosing a path.
const SignedSniffLen = len(signedPlanFamily)

// LooksSigned reports whether b starts like a signed-plan envelope of any
// version ("GONF-SIGNED-PLAN/"). It is the sniff docs/plan-signing.md
// "Verification order" step 1 runs before any other: an input for which it
// is true must go to Verify (which refuses another version or a malformed
// header as ErrEnvelopeMalformed) and never to the unsigned sealed or
// plaintext paths, so an envelope gonf cannot verify is refused instead of
// being decoded some other way. It checks the prefix only and verifies
// nothing.
func LooksSigned(b []byte) bool {
	return bytes.HasPrefix(b, []byte(signedPlanFamily))
}

var (
	// ErrNotSigned is returned by Verify for an input that is not a signed
	// envelope at all. Whether an unsigned input is acceptable is the
	// caller's policy; Verify never passes one through.
	ErrNotSigned = errors.New("plan/seal: not a signed plan: no " + SignedPlanMagic + " envelope")
	// ErrEnvelopeMalformed is returned by Verify for an input that starts
	// like an envelope but is not a well-formed GONF-SIGNED-PLAN/1 one: an
	// unsupported version, a truncated header, a key or signature line of
	// the wrong length or encoding, a signed-at line that is not the one
	// canonical spelling (signedat.go), or a signer key that is not a strong
	// Ed25519 public key (small order or non-canonical, edpoint.go).
	ErrEnvelopeMalformed = errors.New("plan/seal: malformed signed-plan envelope")
	// ErrPayloadNotSealed is returned by Sign, and by Verify, when the
	// payload does not start with the age header line.
	ErrPayloadNotSealed = errors.New("plan/seal: signed-plan payload is not a sealed plan (no age header)")
	// ErrNoTrustedSigners is returned by Verify for an empty trusted set,
	// and by LoadTrustedSigners for a file with no key line.
	ErrNoTrustedSigners = errors.New("plan/seal: no trusted signers: refusing to verify against an empty set")
	// ErrSignatureUnrecognizedSigner is returned by Verify when the
	// envelope's signer key is not in the trusted set. It names no key.
	ErrSignatureUnrecognizedSigner = errors.New("plan/seal: signed by a key that is not a trusted signer")
	// ErrSignatureInvalid is returned by Verify when the envelope names a
	// trusted key but its signature does not verify over the signed-at line
	// and the payload.
	ErrSignatureInvalid = errors.New("plan/seal: signature does not verify")
	// ErrSignerInvalid is returned by Sign for a zero or otherwise
	// incomplete Signer.
	ErrSignerInvalid = errors.New("plan/seal: invalid signer")
	// ErrSignedAtInvalid is returned by SignAt for a signing time the
	// signed-at line cannot represent: the zero time, or a UTC year outside
	// 0000-9999.
	ErrSignedAtInvalid = errors.New("plan/seal: signing time is zero or outside years 0000-9999")
)

// Verified is what a successful Verify returns: the sealed payload, the
// trusted entry whose key signed it, and the signing time the signature
// covers.
type Verified struct {
	// Sealed is the payload exactly as Sign was given it, ready for Open
	// the same as an unsigned plan.age. It aliases the envelope (capacity
	// clipped, so appending to it cannot overwrite the envelope); a caller
	// that keeps mutating the envelope must copy it.
	Sealed []byte
	// Signer is a copy of the trusted entry that matched, Label included.
	Signer TrustedSigner
	// SignedAt is the envelope's signed-at time, in UTC at second
	// precision. It is authenticated (covered by the signature) but not
	// checked against any clock: a freshness window is the caller's policy
	// (docs/plan-signing.md "Replay and rollback", task 8g2).
	SignedAt time.Time
}

// Sign wraps sealed (the complete output of Seal) in a GONF-SIGNED-PLAN/1
// envelope signed by signer and stamped with the current time: SignAt with
// time.Now as the clock.
func Sign(sealed []byte, signer Signer) ([]byte, error) {
	return SignAt(sealed, signer, time.Now())
}

// SignAt is Sign with the signing time given explicitly, so a caller (or a
// test) supplies its own clock. at is converted to UTC and truncated to the
// second, the envelope's only precision; a zero time, or one whose UTC year
// is outside 0000-9999, is refused with ErrSignedAtInvalid, since no real
// clock yields it and the fixed-width line could not hold it.
//
// SignAt performs no encryption and opens nothing: it operates purely on
// the bytes Seal already produced, which it requires to start with the age
// header (ErrPayloadNotSealed otherwise). The returned envelope is a new
// slice; sealed is not modified.
func SignAt(sealed []byte, signer Signer, at time.Time) ([]byte, error) {
	if !signer.valid() {
		return nil, ErrSignerInvalid
	}
	if !bytes.HasPrefix(sealed, []byte(ageHeaderLine)) {
		return nil, ErrPayloadNotSealed
	}
	stamp, err := formatSignedAt(at)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(signer.privateKey(), signedMessage(stamp, sealed))
	pub := signer.Public().Key
	env := make([]byte, 0, envelopeHeaderLen+len(sealed))
	env = append(env, SignedPlanMagic+"\n"...)
	env = keyEncoding.AppendEncode(env, pub)
	env = append(env, '\n')
	env = keyEncoding.AppendEncode(env, sig)
	env = append(env, '\n')
	env = appendSignedAtLine(env, stamp)
	return append(env, sealed...), nil
}

// Verify checks env, a GONF-SIGNED-PLAN/1 envelope, against trusted and,
// only on success, returns the sealed payload, the trusted entry that
// matched and the authenticated signing time (Verified). It performs no
// decryption and enforces no freshness window (see Verified.SignedAt).
//
// The envelope's embedded key is never trusted on its own authority: it is
// compared by exact bytes with each entry of trusted, in order, and the
// signature is then checked with the matching TRUSTED entry's key. Verify
// fails closed on every other outcome, each with its own error and none
// naming key material: an empty trusted set (ErrNoTrustedSigners), an input
// that is not an envelope (ErrNotSigned), a malformed, truncated or
// other-version envelope, one whose signed-at line is not the one canonical
// spelling, or one naming a small-order or non-canonical signer key
// (ErrEnvelopeMalformed), a payload that is not a sealed plan
// (ErrPayloadNotSealed), a key not in trusted, or matching only a trusted
// entry whose own key is weak (ErrSignatureUnrecognizedSigner; findTrusted
// applies the same strongPublicKey check LoadTrustedSigners does, so a
// caller-built entry cannot bypass it) and a signature that does not verify —
// a tampered payload or signed-at time, or a key line swapped for another
// trusted key (ErrSignatureInvalid). A refusal returns the zero Verified.
func Verify(env []byte, trusted []TrustedSigner) (Verified, error) {
	if len(trusted) == 0 {
		return Verified{}, ErrNoTrustedSigners
	}
	e, err := parseEnvelope(env)
	if err != nil {
		return Verified{}, err
	}
	match, ok := findTrusted(trusted, e.pub)
	if !ok {
		return Verified{}, ErrSignatureUnrecognizedSigner
	}
	if !ed25519.Verify(match.Key, signedMessage(e.stamp, e.payload), e.sig) {
		return Verified{}, ErrSignatureInvalid
	}
	return Verified{Sealed: e.payload, Signer: match, SignedAt: e.signedAt}, nil
}

// envelopeHeaderLen is the exact length of everything before the payload.
var envelopeHeaderLen = len(SignedPlanMagic) + 1 +
	keyEncoding.EncodedLen(ed25519.PublicKeySize) + 1 +
	keyEncoding.EncodedLen(ed25519.SignatureSize) + 1 +
	signedAtLineLen

// envelope is a parsed, not yet verified, GONF-SIGNED-PLAN/1 envelope.
// stamp is the signed-at line's timestamp text exactly as it appeared (what
// the signature covers) and signedAt its parsed value; payload aliases the
// input with its capacity clipped.
type envelope struct {
	pub, sig, payload []byte
	stamp             string
	signedAt          time.Time
}

// parseEnvelope splits env into its signer key, signature, signed-at time
// and payload, requiring the exact layout described in the file comment.
func parseEnvelope(env []byte) (envelope, error) {
	rest, ok := bytes.CutPrefix(env, []byte(SignedPlanMagic+"\n"))
	if !ok {
		if LooksSigned(env) {
			return envelope{}, ErrEnvelopeMalformed
		}
		return envelope{}, ErrNotSigned
	}
	var e envelope
	if e.pub, rest, ok = cutKeyLine(rest, ed25519.PublicKeySize); !ok || !strongPublicKey(e.pub) {
		return envelope{}, ErrEnvelopeMalformed
	}
	if e.sig, rest, ok = cutKeyLine(rest, ed25519.SignatureSize); !ok {
		return envelope{}, ErrEnvelopeMalformed
	}
	if e.stamp, e.signedAt, rest, ok = cutSignedAtLine(rest); !ok {
		return envelope{}, ErrEnvelopeMalformed
	}
	if !bytes.HasPrefix(rest, []byte(ageHeaderLine)) {
		return envelope{}, ErrPayloadNotSealed
	}
	e.payload = rest[:len(rest):len(rest)]
	return e, nil
}

// cutKeyLine decodes b's first line, which must be exactly the base64 form
// of size bytes followed by "\n", and returns the decoded bytes and what
// follows the line. ok is false for a missing newline (a truncated
// envelope), a line of any other length, or a non-canonical encoding.
func cutKeyLine(b []byte, size int) (decoded, rest []byte, ok bool) {
	n := keyEncoding.EncodedLen(size)
	if len(b) <= n || b[n] != '\n' {
		return nil, nil, false
	}
	decoded, err := decodeKey(b[:n], size)
	if err != nil {
		return nil, nil, false
	}
	return decoded, b[n+1:], true
}

// findTrusted returns the first entry of trusted whose key is exactly pub.
// An entry whose key is not a strong Ed25519 public key (strongPublicKey,
// edpoint.go) can never match, so neither a malformed caller-built entry
// (which would make ed25519.Verify panic) nor a small-order one (which
// would let anyone forge a signature for it) is ever used, even when a
// caller builds the TrustedSigner itself instead of loading it with
// LoadTrustedSigners.
func findTrusted(trusted []TrustedSigner, pub []byte) (TrustedSigner, bool) {
	for _, t := range trusted {
		if bytes.Equal(t.Key, pub) && strongPublicKey(t.Key) {
			return TrustedSigner{Key: bytes.Clone(t.Key), Label: t.Label}, true
		}
	}
	return TrustedSigner{}, false
}

// signedMessage is the message Sign signs and Verify checks: the magic
// line, the signed-at line for stamp, then the sealed payload (see "What
// the signature covers").
func signedMessage(stamp string, sealed []byte) []byte {
	msg := make([]byte, 0, len(SignedPlanMagic)+1+signedAtLineLen+len(sealed))
	msg = append(msg, SignedPlanMagic+"\n"...)
	msg = appendSignedAtLine(msg, stamp)
	return append(msg, sealed...)
}
