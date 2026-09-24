package seal

import (
	"bytes"
	"crypto/ed25519"
	"errors"
)

// This file is the envelope half of plan signing (docs/plan-signing.md,
// "Recommended design", task 6g2, phase signing-1): Sign wraps the bytes
// Seal produced in a GONF-SIGNED-PLAN/1 envelope carrying an Ed25519
// signature over them, and Verify checks that signature against a
// destination's trusted signers BEFORE anything is decrypted, handing the
// untouched sealed bytes back for Open. Signing is encrypt-then-sign: it
// never looks inside the ciphertext, and Verify performs no decryption.
//
// # Envelope (GONF-SIGNED-PLAN/1)
//
//	GONF-SIGNED-PLAN/1\n
//	<signer public key: 32 bytes, 43 base64 characters>\n
//	<signature: 64 bytes, 86 base64 characters>\n
//	<the complete plan.age bytes, unchanged, to EOF>
//
// base64 is keyEncoding (signer.go): standard alphabet, no padding,
// decoded strictly, and each line must have its exact length, so every
// envelope has exactly one accepted spelling. The payload must itself
// start with age's own header line ("age-encryption.org/v1\n"): Sign
// refuses to sign anything else and Verify refuses to return anything
// else, so a signed PLAINTEXT plan (or a nested envelope) can never come
// out of a successful Verify and be mistaken for a sealed one.
//
// # What the signature covers
//
// The signed message is the magic line followed by the complete payload:
// "GONF-SIGNED-PLAN/1\n" || plan.age. It therefore covers every byte of the
// age header, every recipient stanza and the whole ciphertext
// (docs/plan-signing.md threat S5: a replaced or edited ciphertext
// invalidates it), and the envelope version too, so a signature cannot be
// relabelled under another envelope version or format that signs the bare
// payload. Ed25519 itself binds the public key into the signature, so the
// key line needs no separate coverage. There is no algorithm field to
// confuse: the magic fixes the algorithm (pure Ed25519, RFC 8032), and a
// future algorithm or field change gets a new magic.
//
// # Not in this envelope: freshness
//
// This version carries no signed-at timestamp or counter, so a Verify
// success says only which trusted key produced these exact bytes, not that
// they are current: a replayed old envelope verifies forever
// (docs/plan-signing.md "Replay and rollback"). The freshness check is
// phase signing-3; adding its field changes the envelope and so the
// magic. Nothing in gonf consumes a signed plan yet (no CLI surface).

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

var (
	// ErrNotSigned is returned by Verify for an input that is not a signed
	// envelope at all. Whether an unsigned input is acceptable is the
	// caller's policy; Verify never passes one through.
	ErrNotSigned = errors.New("plan/seal: not a signed plan: no " + SignedPlanMagic + " envelope")
	// ErrEnvelopeMalformed is returned by Verify for an input that starts
	// like an envelope but is not a well-formed GONF-SIGNED-PLAN/1 one: an
	// unsupported version, a truncated header, or a key or signature line
	// of the wrong length or encoding.
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
	// trusted key but its signature does not verify over the payload.
	ErrSignatureInvalid = errors.New("plan/seal: signature does not verify")
	// ErrSignerInvalid is returned by Sign for a zero or otherwise
	// incomplete Signer.
	ErrSignerInvalid = errors.New("plan/seal: invalid signer")
)

// Sign wraps sealed (the complete output of Seal) in a GONF-SIGNED-PLAN/1
// envelope signed by signer. It performs no encryption and opens nothing:
// it operates purely on the bytes Seal already produced, which it requires
// to start with the age header (ErrPayloadNotSealed otherwise). The
// returned envelope is a new slice; sealed is not modified.
func Sign(sealed []byte, signer Signer) ([]byte, error) {
	if !signer.valid() {
		return nil, ErrSignerInvalid
	}
	if !bytes.HasPrefix(sealed, []byte(ageHeaderLine)) {
		return nil, ErrPayloadNotSealed
	}
	sig := ed25519.Sign(signer.key, signedMessage(sealed))
	pub := signer.Public().Key
	env := make([]byte, 0, envelopeHeaderLen+len(sealed))
	env = append(env, SignedPlanMagic+"\n"...)
	env = keyEncoding.AppendEncode(env, pub)
	env = append(env, '\n')
	env = keyEncoding.AppendEncode(env, sig)
	env = append(env, '\n')
	return append(env, sealed...), nil
}

// Verify checks env, a GONF-SIGNED-PLAN/1 envelope, against trusted and,
// only on success, returns the sealed payload exactly as Sign was given it
// (ready for Open, the same as an unsigned plan.age) together with the
// trusted entry that matched. It performs no decryption.
//
// The envelope's embedded key is never trusted on its own authority: it is
// compared by exact bytes with each entry of trusted, in order, and the
// signature is then checked with the matching TRUSTED entry's key. Verify
// fails closed on every other outcome, each with its own error and none
// naming key material: an empty trusted set (ErrNoTrustedSigners), an input
// that is not an envelope (ErrNotSigned), a malformed, truncated or
// other-version envelope (ErrEnvelopeMalformed), a payload that is not a
// sealed plan (ErrPayloadNotSealed), a key not in trusted
// (ErrSignatureUnrecognizedSigner) and a signature that does not verify —
// a tampered payload, or a key line swapped for another trusted key
// (ErrSignatureInvalid).
//
// The returned sealed slice aliases env (capacity clipped, so appending to
// it cannot overwrite env); a caller that keeps mutating env must copy it.
func Verify(env []byte, trusted []TrustedSigner) (sealed []byte, signer TrustedSigner, err error) {
	if len(trusted) == 0 {
		return nil, TrustedSigner{}, ErrNoTrustedSigners
	}
	pub, sig, payload, err := parseEnvelope(env)
	if err != nil {
		return nil, TrustedSigner{}, err
	}
	match, ok := findTrusted(trusted, pub)
	if !ok {
		return nil, TrustedSigner{}, ErrSignatureUnrecognizedSigner
	}
	if !ed25519.Verify(match.Key, signedMessage(payload), sig) {
		return nil, TrustedSigner{}, ErrSignatureInvalid
	}
	return payload, match, nil
}

// envelopeHeaderLen is the exact length of everything before the payload.
var envelopeHeaderLen = len(SignedPlanMagic) + 1 +
	keyEncoding.EncodedLen(ed25519.PublicKeySize) + 1 +
	keyEncoding.EncodedLen(ed25519.SignatureSize) + 1

// parseEnvelope splits env into its signer key, signature and payload,
// requiring the exact layout described in the file comment.
func parseEnvelope(env []byte) (pub, sig, payload []byte, err error) {
	rest, ok := bytes.CutPrefix(env, []byte(SignedPlanMagic+"\n"))
	if !ok {
		if bytes.HasPrefix(env, []byte(signedPlanFamily)) {
			return nil, nil, nil, ErrEnvelopeMalformed
		}
		return nil, nil, nil, ErrNotSigned
	}
	if pub, rest, ok = cutKeyLine(rest, ed25519.PublicKeySize); !ok {
		return nil, nil, nil, ErrEnvelopeMalformed
	}
	if sig, rest, ok = cutKeyLine(rest, ed25519.SignatureSize); !ok {
		return nil, nil, nil, ErrEnvelopeMalformed
	}
	if !bytes.HasPrefix(rest, []byte(ageHeaderLine)) {
		return nil, nil, nil, ErrPayloadNotSealed
	}
	return pub, sig, rest[:len(rest):len(rest)], nil
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
// An entry whose key is not a complete Ed25519 public key can never match
// (pub always is one), so a malformed caller-built entry cannot reach
// ed25519.Verify, which would panic on it.
func findTrusted(trusted []TrustedSigner, pub []byte) (TrustedSigner, bool) {
	for _, t := range trusted {
		if len(t.Key) == ed25519.PublicKeySize && bytes.Equal(t.Key, pub) {
			return TrustedSigner{Key: bytes.Clone(t.Key), Label: t.Label}, true
		}
	}
	return TrustedSigner{}, false
}

// signedMessage is the message Sign signs and Verify checks: the magic
// line followed by the sealed payload (see "What the signature covers").
func signedMessage(sealed []byte) []byte {
	msg := make([]byte, 0, len(SignedPlanMagic)+1+len(sealed))
	msg = append(msg, SignedPlanMagic+"\n"...)
	return append(msg, sealed...)
}
