package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/snonux/gonf/plan/seal"
)

// This file is task 7g2 (signing phase 2, docs/plan-signing.md "Phased
// implementation"): `gonf plan -seal -sign <signer-file>` and `gonf
// plan-signer-keygen <signer-file>`. -sign wraps each sealed artifact the
// -seal paths already build (plain, -stdout and -for, per host) in a
// GONF-SIGNED-PLAN/1 envelope (plan/seal.SignAt) before it is staged or
// printed, so a signed plan.age is written through exactly the same write
// path as an unsigned one. Signing proves only which key produced the
// bytes, and nothing here checks a signature, so every message says
// "signed", never "verified" or "trusted" (docs/plan-signing.md
// "Verification order", plan-encryption.md "Provenance"). The key material
// stays inside plan/seal: this file handles a seal.Signer value, whose
// printed form is only its public key, and never the secret line.

// artifactSigner is -sign's loaded signer. A nil *artifactSigner means
// -sign was not given, and its sign method then passes the sealed bytes
// through unchanged, so the unsigned -seal paths stay byte-identical.
type artifactSigner struct {
	signer seal.Signer
	// now is the signing clock (time.Now; a test may pin it).
	now func() time.Time
}

// artifactSignature is what the "wrote" report says about a signed
// artifact: the signer's public key and the signed-at time the envelope
// carries. A nil *artifactSignature means the artifact is unsigned.
type artifactSignature struct {
	signer seal.TrustedSigner
	at     time.Time
}

// loadArtifactSigner loads -sign's signer file with plan/seal.LoadSigner
// (its no-follow walk, owner and 0o077 mode checks). path == "" means -sign
// was not given: no signer, no error. LoadSigner's errors name the path,
// line and class, never the file's content, so the caller may print them.
func loadArtifactSigner(path string) (*artifactSigner, error) {
	if path == "" {
		return nil, nil
	}
	s, err := seal.LoadSigner(path)
	if err != nil {
		return nil, err
	}
	return &artifactSigner{signer: s, now: time.Now}, nil
}

// sign wraps sealed in a signed envelope stamped with s.now, or, for a nil
// s, returns sealed itself and no signature.
func (s *artifactSigner) sign(sealed []byte) ([]byte, *artifactSignature, error) {
	if s == nil {
		return sealed, nil, nil
	}
	at := s.now().UTC().Truncate(time.Second)
	env, err := seal.SignAt(sealed, s.signer, at)
	if err != nil {
		return nil, nil, fmt.Errorf("sign: %w", err)
	}
	return env, &artifactSignature{signer: s.signer.Public(), at: at}, nil
}

// signedNote is the ", signed <time>" suffix a signed artifact's "wrote"
// summary gets, "" for an unsigned one (so that wording stays unchanged).
// The time is the envelope's own signed-at value (RFC 3339, UTC).
func (a *artifactSignature) signedNote() string {
	if a == nil {
		return ""
	}
	return ", signed " + a.at.Format(time.RFC3339)
}

// detailLine is the "  signer ..." line under a signed artifact's "wrote"
// line, "" for an unsigned one.
func (a *artifactSignature) detailLine() string {
	if a == nil {
		return ""
	}
	return "  signer " + signerDisplay(a.signer) + "\n"
}

// signerDisplay renders a signer's public key for operator output: its
// exact trusted-signers line (the 43-character key is short enough to show
// whole, unlike an age1pq recipient) and a SHA-256 fingerprint of that line,
// the same "sha256:" + 16 hex digits form recipientFingerprint (task 4g2)
// uses, reproducible with `printf %s 'gonf-signer-ed25519 KEY' | sha256sum`.
// Public key only; safe to print.
func signerDisplay(pub seal.TrustedSigner) string {
	pub.Label = ""
	line := pub.String()
	sum := sha256.Sum256([]byte(line))
	return line + " sha256:" + hex.EncodeToString(sum[:])[:16]
}

// cliPlanSignerKeygen is `gonf plan-signer-keygen <signer-file>`: generate a
// new Ed25519 signer (plan/seal.GenerateSigner) and create signer-file for
// it (plan/seal.WriteSignerFile: 0600, never replacing an existing file or
// following a symlink). The matching trusted-signers line goes to stdout
// alone, so it can be appended to a destination's file directly; the
// "wrote" report and the hint go to stderr. The secret is never printed.
func cliPlanSignerKeygen(args []string) int {
	fs := flag.NewFlagSet("plan-signer-keygen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	// The command has no flags, so -h prints the usage line (with its
	// positional argument) instead of flag's empty "Usage of" listing.
	fs.Usage = func() { eprintln("usage: " + planSignerKeygenUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		eprintln("usage: " + planSignerKeygenUsage)
		return 2
	}
	path := fs.Arg(0)
	signer, err := seal.GenerateSigner()
	if err != nil {
		eprintf("plan-signer-keygen: %v\n", err)
		return 1
	}
	if err := seal.WriteSignerFile(path, signer); err != nil {
		eprintf("plan-signer-keygen: %v; nothing written\n", err)
		return 1
	}
	eprintf("wrote signer secret key %s (mode 0600; keep it private)\n  signer %s\n"+
		"add the line below to each destination's trusted-signers file:\n", path, signerDisplay(signer.Public()))
	fmt.Println(signer.Public().String())
	return 0
}

// planSignerKeygenUsage is plan-signer-keygen's usage line, shared by its
// own refusal and printUsage.
const planSignerKeygenUsage = "gonf plan-signer-keygen <signer-file>"
