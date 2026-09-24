// Package seal wraps filippo.io/age to encrypt and decrypt gonf plan
// artifacts (docs/plan-encryption.md, task w82, phase 1 of that design). It
// is the only package in this module that imports filippo.io/age, so CLI
// and api code depend only on this package's own Recipient and Identity
// types, and a later change of cryptographic backend (docs/plan-encryption.md
// option C', the stdlib crypto/hpke fallback) would touch only this
// package.
//
// Its CLI callers are gonf plan -seal and gonf apply -identity
// (internal/cli, tasks 2b2 and 3b2); this package is the one reviewed
// place anything that seals or opens a plan artifact goes through.
//
// It also owns plan signing (docs/plan-signing.md, tasks 6g2 and 7g2):
// Sign/SignAt, Verify, LoadSigner, LoadTrustedSigners and
// GenerateSigner/WriteSignerFile, with their own Signer and TrustedSigner
// types, so crypto/ed25519 is likewise imported here only. The CLI signs
// (`gonf plan -seal -sign`, `gonf plan-signer-keygen`, task 7g2) and
// verifies (`gonf apply -trusted-signers`, `gonf plan-verify`, task 8g2)
// only through these. All four key files the
// package reads (identity, recipients, signer and trusted-signers) share
// one hardened-open policy (keyfile.go).
//
// Besides file-backed operator identities (LoadIdentities), it offers
// GenerateEphemeral, EncodeEphemeral and ParseEphemeral: a single-use,
// in-memory-only identity with a one-line text form for stdin delivery
// (docs/plan-encryption.md, phase 4 design, "Key lifecycle").
//
// # Recipient policy: age1pq (hybrid ML-KEM-768 + X25519) only
//
// age refuses to encrypt one file to both hybrid post-quantum and classic
// X25519 recipients in the same call ("incompatible recipients"), because a
// single classic recipient would undo the post-quantum protection every
// other recipient paid for. This package sidesteps that failure mode
// entirely by never accepting a classic age1, ssh-*, or plugin recipient or
// identity in the first place: ParseRecipients and LoadIdentities enforce
// age1pq-only policy at load time, so a mixed or non-hybrid list can never
// reach [Seal] or [Open] through this package's API. Every refusal names
// the offending line (by number) and its class (classic X25519, ssh,
// unrecognized/plugin, malformed), never any key material — a recipient is
// a public key, but the same habit is what LoadIdentities needs for a
// private one, so both follow it.
//
// # Confidentiality only, never provenance
//
// A file that decrypts with [Open] was made by someone who knew a
// recipient's PUBLIC key, which is everyone the recipient was ever shared
// with (or anyone at all, since recipients are meant to be shared). age has
// no sender authentication, so successful decryption proves nothing about
// who sealed the file, only that whoever did knew a public key. This
// package's tamper tests (truncation, a flipped bit, header damage) prove
// only that CORRUPTION is detected — that age's AEAD notices the ciphertext
// changed after it was sealed. They are not, and must never be read or
// described as, an authenticity or provenance guarantee: anyone can seal a
// fresh, uncorrupted, perfectly valid plan.age to a public recipient and
// substitute it for the real one. See docs/plan-encryption.md, section
// "Provenance". Provenance is a separate, optional layer around the sealed
// bytes: [Sign] and [Verify] (sign.go, docs/plan-signing.md, task 6g2)
// wrap them in an Ed25519-signed GONF-SIGNED-PLAN/1 envelope that a
// destination checks against its own trusted signers before decrypting.
// That library alone lifts nothing: nothing in gonf may apply a sealed
// plan unattended until an entry point meets docs/plan-signing.md's "The
// unblocking condition".
package seal

import (
	"errors"
	"fmt"
	"io"

	"filippo.io/age"
)

// ErrNoRecipients is returned by Seal when given zero recipients, so a
// caller can never silently produce an artifact nobody, not even the
// operator, can open.
var ErrNoRecipients = errors.New("plan/seal: no recipients: refusing to seal an unreadable plan")

// ErrNoIdentities is returned by Open when given zero identities, worded
// distinctly from age's own "no identity matched" so a caller that simply
// forgot to load one is not confused with a genuine wrong-identity case.
var ErrNoIdentities = errors.New("plan/seal: no identities: cannot open a sealed plan")

// Seal returns a WriteCloser that age-encrypts everything written to it,
// streaming the sealed bytes to w, addressed to every recipient in
// recipients. The caller must Close it for the final chunk to be encrypted
// and flushed; Close's error must be checked, since the last authentication
// tag is only computed then.
//
// recipients must be non-empty (ErrNoRecipients otherwise): every member
// came from ParseRecipients, so it is already a validated age1pq hybrid
// recipient and Seal itself performs no further recipient-type checks.
func Seal(w io.Writer, recipients []Recipient) (io.WriteCloser, error) {
	if len(recipients) == 0 {
		return nil, ErrNoRecipients
	}
	wc, err := age.Encrypt(w, toAgeRecipients(recipients)...)
	if err != nil {
		// None of age.Encrypt's own failures here can include recipient or
		// file-key material (they are fixed strings such as the
		// "incompatible recipients" label mismatch, or a write failure on
		// w), so wrapping with %w is safe and keeps errors.Is/As working for
		// callers.
		return nil, fmt.Errorf("plan/seal: seal: %w", err)
	}
	return wc, nil
}

// Open decrypts r, which must be an age stream Seal produced, trying every
// identity in identities until one matches. identities must be non-empty
// (ErrNoIdentities otherwise, rather than the more confusing "no identity
// matched any of the recipients" age itself would report for an empty
// list).
//
// age's AEAD authenticates the final 64 KiB segment only once it has been
// read, at EOF (docs/plan-encryption.md "Failure handling"). A caller that
// must trust the whole plaintext before acting on any part of it — as a
// sealed gonf apply does, one op at a time — has to read Open's Reader to
// EOF and check its error before doing anything with what it already read;
// Open itself does not buffer the stream to do this for the caller, since
// that would defeat streaming for the common, unsealed-blob case.
func Open(r io.Reader, identities []Identity) (io.Reader, error) {
	if len(identities) == 0 {
		return nil, ErrNoIdentities
	}
	rc, err := age.Decrypt(r, toAgeIdentities(identities)...)
	if err != nil {
		// age.Decrypt's failures here (a bad header, no matching identity,
		// a bad MAC) are all fixed, generic wording; none echo plaintext,
		// key or identity material, so wrapping is safe.
		return nil, fmt.Errorf("plan/seal: open: %w", err)
	}
	return rc, nil
}
