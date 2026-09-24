package seal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
)

// Regression tests for the independent review of task 6g2's first commit,
// built from the reviewer's probes (zz_review_probe_test.go and
// zz_review_probe2_test.go in that review's worktree).

// identityKey and zeroKey are the two weak keys the review forged for:
// the identity point (order 1) and the all-zero encoding (order 4, the
// likely placeholder).
func identityKey() []byte { k := make([]byte, 32); k[0] = 1; return k }
func zeroKey() []byte     { return make([]byte, 32) }

// envelopeWith builds an envelope naming key and carrying sig over sealed,
// bypassing Sign (a forger has no Signer).
func envelopeWith(key, sig, sealed []byte) []byte {
	env := []byte(SignedPlanMagic + "\n")
	env = keyEncoding.AppendEncode(env, key)
	env = append(env, '\n')
	env = keyEncoding.AppendEncode(env, sig)
	env = append(env, '\n')
	return append(env, sealed...)
}

// forgeForSmallOrderKey returns a signature over msg that ed25519.Verify
// accepts for the small-order key, made without any private key: R=[S]B
// for a random S, which verifies whenever [k]A is the identity (always
// for the identity point, one try in four for the order-4 zero key). It
// proves each weak-key test below refuses a signature the bare primitive
// really accepts, so the test cannot pass vacuously.
func forgeForSmallOrderKey(t *testing.T, key, msg []byte) []byte {
	t.Helper()
	l, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	for range 256 {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			t.Fatal(err)
		}
		r, _ := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		h := sha512.Sum512(seed)
		s := h[:32]
		s[0] &= 248
		s[31] &= 127
		s[31] |= 64
		sInt := new(big.Int).SetBytes(reversed(s))
		sInt.Mod(sInt, l)
		sig := append([]byte(nil), r...)
		sig = append(sig, reversed(sInt.FillBytes(make([]byte, 32)))...)
		if ed25519.Verify(key, msg, sig) {
			return sig
		}
	}
	t.Fatal("could not forge a signature for the small-order key; the test premise is broken")
	return nil
}

// TestVerifyRefusesForgeryForSmallOrderKey: a caller-built TrustedSigner
// with a small-order key (not loaded through LoadTrustedSigners, so no
// load-time check ran) still never verifies a forged envelope.
func TestVerifyRefusesForgeryForSmallOrderKey(t *testing.T) {
	f := newSignedFixture(t)
	msg := signedMessage(f.sealed)
	for name, key := range map[string][]byte{"identity": identityKey(), "all-zero": zeroKey()} {
		t.Run(name, func(t *testing.T) {
			sig := forgeForSmallOrderKey(t, key, msg)
			env := envelopeWith(key, sig, f.sealed)
			requireRefused(t, env, []TrustedSigner{{Key: key, Label: "placeholder"}}, ErrEnvelopeMalformed)
			if _, ok := findTrusted([]TrustedSigner{{Key: key}}, key); ok {
				t.Fatal("findTrusted matched a small-order caller-built entry")
			}
		})
	}
	// The identity point's trivial forgery (R = identity, S = 0).
	sig := make([]byte, 64)
	sig[0] = 1
	if !ed25519.Verify(identityKey(), msg, sig) {
		t.Fatal("test premise: R=identity, S=0 should verify for the identity key")
	}
	requireRefused(t, envelopeWith(identityKey(), sig, f.sealed), []TrustedSigner{{Key: identityKey()}}, ErrEnvelopeMalformed)
}

func TestLoadTrustedSignersRefusesWeakKeys(t *testing.T) {
	keys := map[string][]byte{"identity": identityKey(), "all-zero": zeroKey()}
	for _, h := range smallOrderBlocklist {
		k, _ := hex.DecodeString(h)
		keys["blocklist "+h[:8]] = k
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			line := signerPublicType + " " + keyEncoding.EncodeToString(key) + " placeholder\n"
			path := writeKeyFile(t, t.TempDir(), "signers", line, 0o644)
			if _, err := LoadTrustedSigners(path); !errors.Is(err, ErrTrustedSignerWeakKey) {
				t.Fatalf("got %v, want ErrTrustedSignerWeakKey", err)
			}
		})
	}
}

// TestSignerNeverPrintsKeyWhenNested is the review's probe P1: fmt cannot
// call Format on a Signer in an unexported struct field and prints its
// fields instead, so the key must not be reachable as a printable value.
func TestSignerNeverPrintsKeyWhenNested(t *testing.T) {
	s, line := genSigner(t)
	priv := s.privateKey()
	decimal := fmt.Sprint([]byte(priv.Seed()))
	forbidden := []string{
		strings.Fields(line)[1],
		fmt.Sprintf("%x", []byte(priv.Seed())),
		decimal[1 : len(decimal)-1],
		fmt.Sprintf("%#x", priv[0]) + ",",
	}
	type unexported struct{ signer Signer }
	type exported struct{ Signer Signer }
	for _, verb := range []string{"%v", "%+v", "%#v", "%x", "%s", "%d"} {
		for _, v := range []any{unexported{s}, &unexported{s}, exported{s}, &exported{s}, []unexported{{s}}} {
			out := fmt.Sprintf(verb, v)
			for _, bad := range forbidden {
				if strings.Contains(out, bad) {
					t.Fatalf("%s of %T prints private key material: %s", verb, v, out)
				}
			}
		}
	}
}

// TestLoadTrustedSignersRefusesFoldedAndAmbiguousLines covers the review's
// probes P3 and P6: nothing may fold a second entry into a label, a key
// may not repeat, and white space other than ASCII space and tab (and a
// CRLF file's CR) is refused rather than treated as a separator.
func TestLoadTrustedSignersRefusesFoldedAndAmbiguousLines(t *testing.T) {
	a, _ := genSigner(t)
	b, _ := genSigner(t)
	pa, pb := a.Public().String(), b.Public().String()
	keyB := strings.Fields(pb)[1]
	cases := map[string]struct {
		content string
		want    error
	}{
		"CR-only file":           {pa + "\r" + pb + "\r", ErrTrustedSignerMalformed},
		"two keys on one line":   {pa + " " + pb + "\n", ErrTrustedSignerMalformed},
		"key-shaped label":       {pa + " backup " + keyB + "\n", ErrTrustedSignerMalformed},
		"secret type in label":   {pa + " " + signerSecretType + "\n", ErrTrustedSignerMalformed},
		"duplicate key":          {pa + " ok\n" + a.Public().String() + " dup\n", ErrTrustedSignerDuplicate},
		"NBSP":                   {pa + " a\u00a0nbsp\n", ErrTrustedSignerMalformed},
		"ideographic space":      {pa + " \u3000wide\n", ErrTrustedSignerMalformed},
		"NEL":                    {pa + " a\u0085nel\n", ErrTrustedSignerMalformed},
		"trailing NBSP":          {pa + " label \n", ErrTrustedSignerMalformed},
		"zero-width space":       {pa + " a\u200bzw\n", ErrTrustedSignerMalformed},
		"right-to-left override": {pa + " a\u202eevil\n", ErrTrustedSignerMalformed},
		"NBSP separator":         {signerPublicType + "\u00a0" + strings.Fields(pa)[1] + "\n", ErrTrustedSignerRefused},
		"BOM":                    {"\xef\xbb\xbf" + pa + "\n", ErrKeyFileBOM},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeKeyFile(t, t.TempDir(), "signers", tc.content, 0o644)
			if _, err := LoadTrustedSigners(path); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestKeyFilesAcceptTabsAndCRLF: the separators and line endings that ARE
// accepted still load (so the refusals above are not over-broad).
func TestKeyFilesAcceptTabsAndCRLF(t *testing.T) {
	s, secret := genSigner(t)
	dir := t.TempDir()
	pub := strings.Replace(s.Public().String(), " ", "\t", 1)
	if got, err := LoadTrustedSigners(writeKeyFile(t, dir, "signers", pub+"\t lab el \r\n", 0o644)); err != nil || got[0].Label != "lab el" {
		t.Fatalf("tab/CRLF trusted-signers file: %v, %v", got, err)
	}
	if _, err := LoadSigner(writeKeyFile(t, dir, "signer", strings.Replace(secret, " ", "\t", 1)+"\r\n", 0o600)); err != nil {
		t.Fatalf("tab/CRLF signer file: %v", err)
	}
	if _, err := LoadSigner(writeKeyFile(t, dir, "signer-bom", "\xef\xbb\xbf"+secret+"\n", 0o600)); !errors.Is(err, ErrKeyFileBOM) {
		t.Fatalf("signer file with BOM: got %v, want ErrKeyFileBOM", err)
	}
}

// TestVerifyRefusesEveryHeaderByteMutation is the review's probe P4: every
// single-byte flip, insertion and deletion across the envelope header and
// the start of the age header fails, as does a trailing newline and the
// malleable S+L spelling of a valid signature.
func TestVerifyRefusesEveryHeaderByteMutation(t *testing.T) {
	f := newSignedFixture(t)
	end := min(envelopeHeaderLen+len(ageHeaderLine)+64, len(f.env))
	for i := range end {
		for _, d := range []byte{1, 0x20, 0x80, 0xff} {
			m := bytes.Clone(f.env)
			m[i] ^= d
			if _, _, err := Verify(m, f.trusted()); err == nil {
				t.Fatalf("flip at %d (^%#x) verified", i, d)
			}
		}
		ins := append(append(bytes.Clone(f.env[:i]), ' '), f.env[i:]...)
		del := append(bytes.Clone(f.env[:i]), f.env[i+1:]...)
		for name, m := range map[string][]byte{"insertion": ins, "deletion": del} {
			if _, _, err := Verify(m, f.trusted()); err == nil {
				t.Fatalf("%s at %d verified", name, i)
			}
		}
	}
	if _, _, err := Verify(append(bytes.Clone(f.env), '\n'), f.trusted()); err == nil {
		t.Fatal("trailing newline verified")
	}
	requireRefused(t, withSignature(f, sigPlusL(t, f)), f.trusted(), ErrSignatureInvalid)
}

// sigPlusL returns f's signature with L (the group order) added to S: the
// same value mod L, which a lax verifier would accept.
func sigPlusL(t *testing.T, f signedFixture) []byte {
	t.Helper()
	off := len(SignedPlanMagic) + 1 + 44
	raw, err := keyEncoding.DecodeString(string(f.env[off : off+86]))
	if err != nil {
		t.Fatal(err)
	}
	l, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	s := new(big.Int).SetBytes(reversed(raw[32:]))
	s.Add(s, l)
	return append(bytes.Clone(raw[:32]), reversed(s.FillBytes(make([]byte, 32)))...)
}
