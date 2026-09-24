package seal

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/sha512"
	"errors"
	"io"
	"strings"
	"testing"
)

// signedFixture seals a small plan to a fresh recipient and signs it,
// returning everything a test needs to verify or tamper with it.
type signedFixture struct {
	signer   Signer
	identity Identity
	sealed   []byte
	env      []byte
}

func newSignedFixture(t *testing.T) signedFixture {
	t.Helper()
	signer, _ := genSigner(t)
	recipient, identity := genKeyPair(t)
	sealed := sealBytes(t, "GONF-PUSH/1 plan bytes", []Recipient{recipient})
	env, err := Sign(sealed, signer)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return signedFixture{signer: signer, identity: identity, sealed: sealed, env: env}
}

func (f signedFixture) trusted() []TrustedSigner { return []TrustedSigner{f.signer.Public()} }

// requireRefused asserts Verify fails with want and returns nothing usable.
func requireRefused(t *testing.T, env []byte, trusted []TrustedSigner, want error) {
	t.Helper()
	sealed, who, err := Verify(env, trusted)
	if !errors.Is(err, want) {
		t.Fatalf("Verify: got %v, want %v", err, want)
	}
	if sealed != nil || who.Key != nil || who.Label != "" {
		t.Fatalf("refused Verify still returned data: %d bytes, signer %v", len(sealed), who)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	f := newSignedFixture(t)
	other, _ := genSigner(t)
	mine := f.signer.Public()
	mine.Label = "operator"
	sealed, who, err := Verify(f.env, []TrustedSigner{other.Public(), mine})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !bytes.Equal(sealed, f.sealed) {
		t.Fatal("Verify did not return the exact sealed bytes Sign was given")
	}
	if who.Label != "operator" || !bytes.Equal(who.Key, mine.Key) {
		t.Fatalf("Verify matched %v, want the operator entry", who)
	}
	// The unwrapped bytes are an ordinary plan.age: Open reads them.
	r, err := Open(bytes.NewReader(sealed), []Identity{f.identity})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil || string(plain) != "GONF-PUSH/1 plan bytes" {
		t.Fatalf("Open read %q, %v", plain, err)
	}
}

func TestSignEnvelopeLayout(t *testing.T) {
	f := newSignedFixture(t)
	lines := strings.SplitN(string(f.env), "\n", 4)
	if lines[0] != SignedPlanMagic || len(lines[1]) != 43 || len(lines[2]) != 86 {
		t.Fatalf("unexpected header lines %q", lines[:3])
	}
	if lines[1] != strings.Fields(f.signer.Public().String())[1] {
		t.Fatal("envelope key line is not the signer's public key")
	}
	if !bytes.HasSuffix(f.env, f.sealed) || len(f.env) != envelopeHeaderLen+len(f.sealed) {
		t.Fatal("envelope payload is not the untouched sealed bytes")
	}
}

func TestVerifyRefusesWrongSigner(t *testing.T) {
	f := newSignedFixture(t)
	other, _ := genSigner(t)
	requireRefused(t, f.env, []TrustedSigner{other.Public()}, ErrSignatureUnrecognizedSigner)
}

func TestVerifyRefusesEmptyTrustedSet(t *testing.T) {
	f := newSignedFixture(t)
	requireRefused(t, f.env, nil, ErrNoTrustedSigners)
	requireRefused(t, f.env, []TrustedSigner{}, ErrNoTrustedSigners)
}

// TestVerifyIgnoresMalformedTrustedEntries proves a caller-built entry with
// a short or nil key neither matches nor panics ed25519.Verify.
func TestVerifyIgnoresMalformedTrustedEntries(t *testing.T) {
	f := newSignedFixture(t)
	pub := f.signer.Public().Key
	bad := []TrustedSigner{{}, {Key: pub[:31]}, {Key: append(bytes.Clone(pub), 0)}}
	requireRefused(t, f.env, bad, ErrSignatureUnrecognizedSigner)
	if _, _, err := Verify(f.env, append(bad, f.signer.Public())); err != nil {
		t.Fatalf("Verify with a valid entry after malformed ones: %v", err)
	}
}

// TestVerifyRefusesTamperedPayload flips one bit at several places in the
// sealed payload (age header, recipient stanza, ciphertext, final byte)
// and appends and truncates bytes: every change to what was signed fails.
func TestVerifyRefusesTamperedPayload(t *testing.T) {
	f := newSignedFixture(t)
	n := len(f.sealed)
	for _, off := range []int{len(ageHeaderLine) - 2, len(ageHeaderLine) + 5, n / 2, n - 1} {
		env := bytes.Clone(f.env)
		env[envelopeHeaderLen+off] ^= 0x01
		requireRefused(t, env, f.trusted(), wantForPayloadOffset(off))
	}
	requireRefused(t, append(bytes.Clone(f.env), 'x'), f.trusted(), ErrSignatureInvalid)
	requireRefused(t, f.env[:len(f.env)-1], f.trusted(), ErrSignatureInvalid)
}

// wantForPayloadOffset: a flipped bit inside the age header line itself is
// caught before the signature check, as a payload that is not sealed.
func wantForPayloadOffset(off int) error {
	if off < len(ageHeaderLine) {
		return ErrPayloadNotSealed
	}
	return ErrSignatureInvalid
}

// TestVerifyRefusesReplacedPayload is docs/plan-signing.md threat S5: a
// valid signature kept, with a different, validly sealed ciphertext.
func TestVerifyRefusesReplacedPayload(t *testing.T) {
	f := newSignedFixture(t)
	recipient, _ := genKeyPair(t)
	other := sealBytes(t, "attacker plan", []Recipient{recipient})
	env := append(bytes.Clone(f.env[:envelopeHeaderLen]), other...)
	requireRefused(t, env, f.trusted(), ErrSignatureInvalid)
}

func TestVerifyRefusesTamperedSignature(t *testing.T) {
	f := newSignedFixture(t)
	env := bytes.Clone(f.env)
	sigAt := len(SignedPlanMagic) + 1 + 43 + 1 + 40
	env[sigAt] = flipBase64(env[sigAt])
	requireRefused(t, env, f.trusted(), ErrSignatureInvalid)
}

// TestVerifyRefusesSwappedKeyLine: the envelope names a different trusted
// key than the one that signed. The trusted key is used, so it fails.
func TestVerifyRefusesSwappedKeyLine(t *testing.T) {
	f := newSignedFixture(t)
	other, _ := genSigner(t)
	env := bytes.Clone(f.env)
	copy(env[len(SignedPlanMagic)+1:], strings.Fields(other.Public().String())[1])
	requireRefused(t, env, []TrustedSigner{other.Public(), f.signer.Public()}, ErrSignatureInvalid)
}

// TestVerifyRefusesEveryTruncation cuts the envelope at every length
// short of whole: none verifies, whether the cut lands in the magic, a key
// or signature line, the age header, or the ciphertext.
func TestVerifyRefusesEveryTruncation(t *testing.T) {
	f := newSignedFixture(t)
	for n := range len(f.env) {
		if _, _, err := Verify(f.env[:n], f.trusted()); err == nil {
			t.Fatalf("Verify accepted the envelope truncated to %d of %d bytes", n, len(f.env))
		}
	}
	requireRefused(t, f.env[:envelopeHeaderLen-1], f.trusted(), ErrEnvelopeMalformed)
	requireRefused(t, f.env[:envelopeHeaderLen], f.trusted(), ErrPayloadNotSealed)
}

// TestVerifyRefusesAlgorithmConfusion covers signatures and envelopes that
// differ from pure Ed25519 over the GONF-SIGNED-PLAN/1 message only in how
// they were made or labelled: each must be refused, never reinterpreted.
func TestVerifyRefusesAlgorithmConfusion(t *testing.T) {
	f := newSignedFixture(t)
	msg := signedMessage(f.sealed)
	digest := sha512.Sum512(msg)
	ctxSig := mustSign(t, f.signer, msg, &ed25519.Options{Context: SignedPlanMagic})
	phSig := mustSign(t, f.signer, digest[:], &ed25519.Options{Hash: crypto.SHA512})
	bareSig := ed25519.Sign(f.signer.privateKey(), f.sealed) // no magic prefix: no domain separation
	for name, sig := range map[string][]byte{"Ed25519ctx": ctxSig, "Ed25519ph": phSig, "unprefixed message": bareSig} {
		t.Run(name, func(t *testing.T) {
			requireRefused(t, withSignature(f, sig), f.trusted(), ErrSignatureInvalid)
		})
	}
	relabel := func(from, to string) []byte {
		return []byte(strings.Replace(string(f.env), from, to, 1))
	}
	keyLine := strings.SplitN(string(f.env), "\n", 3)[1]
	for name, env := range map[string][]byte{
		"other version":  relabel(SignedPlanMagic+"\n", "GONF-SIGNED-PLAN/2\n"),
		"longer version": relabel(SignedPlanMagic+"\n", SignedPlanMagic+"0\n"),
		"CRLF lines":     relabel(SignedPlanMagic+"\n", SignedPlanMagic+"\r\n"),
		"padded key":     relabel(keyLine+"\n", keyLine+"=\n"),
		// The line terminators are not signed bytes, so only the parser's
		// exact-layout check keeps them from being malleable.
		"key line terminator": relabel(keyLine+"\n", keyLine+" "),
		"signature line terminator": func() []byte {
			env := bytes.Clone(f.env)
			env[envelopeHeaderLen-1] = ' '
			return env
		}(),
	} {
		t.Run(name, func(t *testing.T) { requireRefused(t, env, f.trusted(), ErrEnvelopeMalformed) })
	}
	t.Run("non-canonical key", func(t *testing.T) {
		env := bytes.Clone(f.env)
		last := len(SignedPlanMagic) + 1 + 42 // the key line's final character
		env[last] = nonCanonical(string(env[last : last+1]))[0]
		requireRefused(t, env, f.trusted(), ErrEnvelopeMalformed)
	})
}

func mustSign(t *testing.T, s Signer, msg []byte, opts crypto.SignerOpts) []byte {
	t.Helper()
	sig, err := s.privateKey().Sign(nil, msg, opts)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

// withSignature returns f's envelope with its signature line replaced.
func withSignature(f signedFixture, sig []byte) []byte {
	env := bytes.Clone(f.env)
	copy(env[len(SignedPlanMagic)+1+43+1:], keyEncoding.EncodeToString(sig))
	return env
}

// flipBase64 returns a different base64 character than c.
func flipBase64(c byte) byte {
	if c == 'A' {
		return 'B'
	}
	return 'A'
}

// TestVerifyRefusesUnsignedInput: a bare plan.age, a plaintext plan and
// empty input are not envelopes, and Verify never passes them through.
func TestVerifyRefusesUnsignedInput(t *testing.T) {
	f := newSignedFixture(t)
	for name, in := range map[string][]byte{
		"bare sealed": f.sealed, "plaintext plan": []byte(`{"kind":"file"}` + "\n"), "empty": nil,
	} {
		t.Run(name, func(t *testing.T) { requireRefused(t, in, f.trusted(), ErrNotSigned) })
	}
}

// TestVerifyRefusesSignedNonSealedPayload: a correctly signed envelope
// whose payload is a plaintext plan (built by hand, since Sign refuses
// it) still fails, so Verify can never hand back plaintext as "sealed".
func TestVerifyRefusesSignedNonSealedPayload(t *testing.T) {
	f := newSignedFixture(t)
	for _, payload := range [][]byte{[]byte(`{"kind":"file"}` + "\n"), f.env} {
		sig := ed25519.Sign(f.signer.privateKey(), signedMessage(payload))
		env := append(bytes.Clone(f.env[:envelopeHeaderLen]), payload...)
		env = withSignature(signedFixture{env: env}, sig)
		requireRefused(t, env, f.trusted(), ErrPayloadNotSealed)
	}
}

func TestSignRefusesNonSealedInput(t *testing.T) {
	f := newSignedFixture(t)
	for name, in := range map[string][]byte{
		"plaintext": []byte(`{"kind":"file"}`), "empty": nil, "nested envelope": f.env,
	} {
		t.Run(name, func(t *testing.T) {
			if env, err := Sign(in, f.signer); !errors.Is(err, ErrPayloadNotSealed) || env != nil {
				t.Fatalf("Sign: got %d bytes, %v, want ErrPayloadNotSealed", len(env), err)
			}
		})
	}
}

func TestSignRefusesInvalidSigner(t *testing.T) {
	f := newSignedFixture(t)
	for _, s := range []Signer{{}, newSigner(f.signer.privateKey()[:32])} {
		if _, err := Sign(f.sealed, s); !errors.Is(err, ErrSignerInvalid) {
			t.Fatalf("Sign with an invalid signer: got %v, want ErrSignerInvalid", err)
		}
	}
}

// TestSignLeavesInputAndVerifyOutputIsolated: Sign does not modify sealed,
// and appending to Verify's result cannot overwrite the envelope.
func TestSignLeavesInputAndVerifyOutputIsolated(t *testing.T) {
	f := newSignedFixture(t)
	before := bytes.Clone(f.sealed)
	if _, err := Sign(f.sealed, f.signer); err != nil || !bytes.Equal(f.sealed, before) {
		t.Fatalf("Sign modified its input or failed: %v", err)
	}
	env := append(bytes.Clone(f.env), "tail"...)[:len(f.env)]
	sealed, _, err := Verify(env, f.trusted())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if cap(sealed) != len(sealed) {
		t.Fatalf("Verify's result has spare capacity %d, so an append would write into the caller's buffer", cap(sealed)-len(sealed))
	}
	grown := append(sealed, "XXXX"...)
	if string(env[:len(f.env)+4][len(f.env):]) != "tail" || !bytes.HasSuffix(grown, []byte("XXXX")) {
		t.Fatal("appending to Verify's result overwrote memory beyond the envelope")
	}
}

// TestSignVerifyErrorsNameNoKeyMaterial checks every Verify refusal's text
// for the signer's key and signature lines.
func TestSignVerifyErrorsNameNoKeyMaterial(t *testing.T) {
	f := newSignedFixture(t)
	lines := strings.SplitN(string(f.env), "\n", 4)
	other, _ := genSigner(t)
	for _, trusted := range [][]TrustedSigner{nil, {other.Public()}} {
		_, _, err := Verify(f.env, trusted)
		if err == nil || strings.Contains(err.Error(), lines[1]) || strings.Contains(err.Error(), lines[2]) {
			t.Fatalf("Verify error names key material or is nil: %v", err)
		}
	}
}
