package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/snonux/gonf/plan/seal"
)

// This file is the verifying half of plan signing (task 8g2, phase
// signing-3; docs/design/plan-signing.md "Verification order and interaction with
// existing decryption"): the signed-plan policy `gonf apply` and `gonf
// plan-verify` share. It runs IN FRONT of the existing sealed-apply path
// (tasks 3b2/be2), which it does not change:
//
//  1. Sniff the input. A GONF-SIGNED-PLAN/ prefix (any version) is the
//     signed path; age-encryption.org/v1 is the unsigned sealed path;
//     anything else is the plaintext path. With -require-signed, the last
//     two are refused outright: nothing is decrypted or applied.
//  2. Signed path: load the destination's own trusted signers, then
//     plan/seal.Verify the envelope BEFORE any age.Decrypt, and only once
//     the signature verified, check the signed-at time against this host's
//     clock (the freshness window). Any failure refuses, exit 1.
//  3. The verified payload is the exact plan.age bytes -seal wrote, handed
//     to the unchanged sealed path (decrypt with -identity, read to EOF
//     under maxSealedFrameBytes, decode, apply).
//
// A signed envelope is never unwrapped without verification, not even
// without -trusted-signers or -require-signed: signing is opt-in for the
// producer, but once a plan carries a signature, dropping it unchecked
// would let a refused or stale plan through by merely not asking. Without
// -trusted-signers the default trusted-signers file is used, with the same
// root rule as -identity (none for root). An unsigned sealed or plaintext
// plan without -require-signed takes the old paths, byte for byte.
//
// Refusals say what failed and never "verified" about anything that was not
// checked (plan-encryption.md "Provenance"); only after Verify and the
// freshness check pass does a line say the signature was verified. Errors
// name paths, line numbers and classes, never key material or plaintext
// (nothing is decrypted before a refusal can happen).

// defaultMaxSignedAge is -max-signed-age's default: how old a signed plan's
// signed-at time may be, by this host's clock, and still be applied. A day
// is the upper end of docs/design/plan-signing.md "Replay and rollback"'s "an hour
// to a day": long enough for a plan signed in the morning to be applied
// that evening or the next morning, short enough that a restored backup of
// last week's plan is refused. An unattended entry point that applies more
// often should pass a shorter window.
const defaultMaxSignedAge = 24 * time.Hour

// signedClockSkew is how far a signed-at time may lie in the future of this
// host's clock and still be accepted: five minutes, the tolerance Kerberos
// uses by default, for a signer whose clock runs slightly ahead. Anything
// further ahead is refused, since an envelope dated in the future would stay
// "fresh" for longer than the window allows. Not tunable: the window
// (-max-signed-age) is the operator's knob, and a clock that is minutes
// off needs fixing, not a wider tolerance.
const signedClockSkew = 5 * time.Minute

// maxSignedEnvelopeBytes bounds a signed envelope read from stdin before it
// is verified. The envelope is the header (about 200 bytes) plus the
// still-encrypted plan.age, whose decrypted frame maxSealedFrameBytes
// already caps: age adds 16 bytes per 64 KiB chunk (1/4096) plus its
// header (about 1.5 KiB per recipient), so this allows 1/1024 plus 1 MiB of
// slack on top, and a legitimate envelope is never refused by it.
const maxSignedEnvelopeBytes = maxSealedFrameBytes + maxSealedFrameBytes/1024 + 1<<20

// signedPlanNow is the clock the freshness window is checked against:
// time.Now, swapped only by this package's tests (setSignedPlanClock).
var signedPlanNow = time.Now

// errSignedPlanStale and errSignedPlanFuture are the freshness-window
// refusals; both are only reached after the signature verified.
var (
	errSignedPlanStale  = errors.New("signed plan is too old")
	errSignedPlanFuture = errors.New("signed plan is dated in the future")
	// errSignedEnvelopeTooLarge refuses a stdin envelope over
	// maxSignedEnvelopeBytes before any of it is verified.
	errSignedEnvelopeTooLarge = errors.New("signed plan exceeds size limit")
)

// signingFlags is the signed-plan policy parsed from -trusted-signers,
// -max-signed-age and (gonf apply only) -require-signed.
type signingFlags struct {
	trustedPaths  stringSliceFlag
	requireSigned bool
	maxAge        time.Duration
}

// registerSigningFlags defines the signing flags on fs and returns where
// they parse into. withRequire adds -require-signed (gonf apply; gonf
// plan-verify only ever accepts signed input).
func registerSigningFlags(fs *flag.FlagSet, withRequire bool) *signingFlags {
	s := &signingFlags{}
	fs.Var(&s.trustedPaths, "trusted-signers", "trusted-signers file to verify a signed plan "+
		"(GONF-SIGNED-PLAN/1) against (repeatable; one 'gonf-signer-ed25519 <key> [label]' line per signer, "+
		"# comments allowed; must be a regular file owned by you, not writable by group or other, reached "+
		"without a symlink); default for a non-root invocation: ${XDG_CONFIG_HOME:-$HOME/.config}/gonf/"+
		"trusted-signers; as root (sudo/doas) it is required. A signed plan is always verified before it is "+
		"decrypted")
	fs.DurationVar(&s.maxAge, "max-signed-age", defaultMaxSignedAge, "refuse a signed plan whose signed-at "+
		"time is older than this by this host's clock (or more than "+signedClockSkew.String()+" in its future)")
	if withRequire {
		fs.BoolVar(&s.requireSigned, "require-signed", false, "refuse any input that is not a signed plan "+
			"(an unsigned plan.age, a plan.jsonl, a push frame): nothing is decrypted or applied")
	}
	return s
}

// conflict returns why the parsed signing flags are unusable, or "".
func (s *signingFlags) conflict() string {
	if s.maxAge <= 0 {
		return "-max-signed-age must be positive"
	}
	for _, p := range s.trustedPaths {
		if p == "" {
			return "-trusted-signers needs a file path"
		}
	}
	return ""
}

// planInputKind is what the first bytes of an apply input say it is.
type planInputKind int

const (
	plainInput  planInputKind = iota // plan.jsonl, a push frame or bare JSONL
	sealedInput                      // an unsigned plan.age
	signedInput                      // a GONF-SIGNED-PLAN/ envelope, any version
)

// classifyPlanInput sniffs head (a whole file, or a peek of at least
// planSniffLen bytes). The signed check comes first, as the design orders
// it; the sealed and plaintext checks are the existing ones.
func classifyPlanInput(head []byte) planInputKind {
	switch {
	case seal.LooksSigned(head):
		return signedInput
	case isSealedPlanBytes(head):
		return sealedInput
	}
	return plainInput
}

// planSniffLen is how many bytes the stdin path peeks: enough for both the
// age header line and the signed-plan prefix.
var planSniffLen = max(len(ageMagicLine), seal.SignedSniffLen)

// verifySignedPlan loads the trusted signers and verifies env against them
// (plan/seal.Verify, which decrypts nothing), then, and only then, checks
// the authenticated signed-at time against the freshness window.
func verifySignedPlan(env []byte, s *signingFlags) (seal.Verified, error) {
	trusted, err := loadTrustedSignerSet(s.trustedPaths)
	if err != nil {
		return seal.Verified{}, err
	}
	v, err := seal.Verify(env, trusted)
	if err != nil {
		return seal.Verified{}, err
	}
	if err := checkSignedFreshness(v.SignedAt, signedPlanNow(), s.maxAge); err != nil {
		return seal.Verified{}, err
	}
	return v, nil
}

// loadTrustedSignerSet loads every -trusted-signers file through
// plan/seal.LoadTrustedSigners (its no-follow walk, owner check and 0o022
// write-bit refusal, task 6g2) and returns the union. Without a path it
// uses the non-root default; root (euid 0) gets no default, exactly as for
// -identity (loadSealedIdentities): under sudo/doas HOME and
// XDG_CONFIG_HOME may belong to either user.
func loadTrustedSignerSet(paths []string) ([]seal.TrustedSigner, error) {
	if len(paths) == 0 {
		if geteuid() == 0 {
			return nil, errors.New("running as root (euid 0); -trusted-signers is required " +
				"(sudo/doas leaves HOME/XDG_CONFIG_HOME ambiguous, so no default trusted-signers path is " +
				"guessed) — pass -trusted-signers <file>")
		}
		def, err := defaultGonfConfigPath("trusted-signers")
		if err != nil {
			return nil, fmt.Errorf("trusted signers: %w", err)
		}
		paths = []string{def}
	}
	var trusted []seal.TrustedSigner
	for _, p := range paths {
		loaded, err := seal.LoadTrustedSigners(p)
		if err != nil {
			return nil, err
		}
		trusted = append(trusted, loaded...)
	}
	return trusted, nil
}

// checkSignedFreshness refuses an authenticated signed-at time at that is
// more than maxAge before now (stale: a replayed or restored plan) or more
// than signedClockSkew after it (future-dated). Exactly maxAge old is still
// accepted. Both errors name the times, which are not secret.
func checkSignedFreshness(at, now time.Time, maxAge time.Duration) error {
	now = now.UTC().Truncate(time.Second)
	switch {
	case now.Sub(at) > maxAge:
		return fmt.Errorf("%w: signed at %s, more than -max-signed-age %s before this host's clock (%s)",
			errSignedPlanStale, at.Format(time.RFC3339), maxAge, now.Format(time.RFC3339))
	case at.Sub(now) > signedClockSkew:
		return fmt.Errorf("%w: signed at %s, more than %s after this host's clock (%s)",
			errSignedPlanFuture, at.Format(time.RFC3339), signedClockSkew, now.Format(time.RFC3339))
	}
	return nil
}

// verifiedNote is the line printed (stderr) once a signed plan's signature
// verified and its signed-at time is inside the window, before anything is
// decrypted. It says exactly what was checked: the signer (public key,
// fingerprint and the trusted-signers label, if any) and the signing time.
func verifiedNote(v seal.Verified, maxAge time.Duration) string {
	label := ""
	if v.Signer.Label != "" {
		label = " (" + v.Signer.Label + ")"
	}
	return fmt.Sprintf("signature verified: trusted signer %s%s, signed %s (within -max-signed-age %s)",
		signerDisplay(v.Signer), label, v.SignedAt.Format(time.RFC3339), maxAge)
}

// admitPlanInput applies the signing policy to one apply input, src naming
// it in messages. It returns the bytes the existing dispatch continues with
// (the verified plan.age for a signed input, data itself otherwise) and
// true, or prints the refusal and returns false (exit 1, nothing decrypted
// or applied).
func admitPlanInput(src string, data []byte, kind planInputKind, s *signingFlags) ([]byte, bool) {
	switch {
	case kind == signedInput:
		v, err := verifySignedPlan(data, s)
		if err != nil {
			eprintf("apply: %s: signed plan refused: %v; nothing decrypted or applied\n", src, err)
			return nil, false
		}
		eprintf("apply: %s: %s\n", src, verifiedNote(v, s.maxAge))
		return v.Sealed, true
	case s.requireSigned:
		refuseUnsigned(src, kind)
		return nil, false
	case len(s.trustedPaths) > 0:
		eprintf("apply: warning: %s is not a signed plan; -trusted-signers only checks signed plans "+
			"(add -require-signed to refuse unsigned input)\n", src)
	}
	return data, true
}

// refuseUnsigned prints -require-signed's refusal of an unsigned input.
func refuseUnsigned(src string, kind planInputKind) {
	what, nothing := "a plaintext plan (plan.jsonl, push frame or bare JSONL)", "nothing applied"
	if kind == sealedInput {
		what, nothing = "an unsigned sealed plan.age", "nothing decrypted or applied"
	}
	eprintf("apply: -require-signed: %s is %s, not a %s envelope; %s\n", src, what, seal.SignedPlanMagic, nothing)
}

// readSignedStream reads a whole signed envelope from r (stdin), refusing
// once it exceeds maxSignedEnvelopeBytes. Verify needs every byte (the
// signature covers the whole plan.age), so the stream is read to EOF first;
// nothing of it is used before Verify succeeds.
func readSignedStream(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read signed plan: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w (%d byte limit)", errSignedEnvelopeTooLarge, limit)
	}
	return data, nil
}
