package seal

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

// Tests for the envelope's signed-at line (signedat.go, task 7g2).

// envelopeWithLine builds f's envelope with lineText (without its "\n") as
// the signed-at line, CORRECTLY signed over it by f's signer. A refusal of
// such an envelope therefore comes from the parser, not the signature.
func envelopeWithLine(f signedFixture, lineText string) []byte {
	msg := append([]byte(SignedPlanMagic+"\n"+lineText+"\n"), f.sealed...)
	sig := ed25519.Sign(f.signer.privateKey(), msg)
	env := []byte(SignedPlanMagic + "\n")
	env = keyEncoding.AppendEncode(env, f.signer.Public().Key)
	env = append(env, '\n')
	env = keyEncoding.AppendEncode(env, sig)
	env = append(env, '\n')
	env = append(env, lineText+"\n"...)
	return append(env, f.sealed...)
}

// TestSignAtStampsUTCSeconds: a local, sub-second time is written as its
// UTC second, and Verify hands back exactly that instant.
func TestSignAtStampsUTCSeconds(t *testing.T) {
	f := newSignedFixture(t)
	zone := time.FixedZone("CEST", 2*60*60)
	at := time.Date(2026, 9, 24, 12, 50, 51, 999_999_999, zone)
	env, err := SignAt(f.sealed, f.signer, at)
	if err != nil {
		t.Fatalf("SignAt: %v", err)
	}
	if got := string(env[signedAtLineOff:envelopeHeaderLen]); got != "signed-at 2026-09-24T10:50:51Z\n" {
		t.Fatalf("signed-at line %q", got)
	}
	v, err := Verify(env, f.trusted())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if want := at.UTC().Truncate(time.Second); !v.SignedAt.Equal(want) || v.SignedAt.Location() != time.UTC {
		t.Fatalf("SignedAt %v, want %v in UTC", v.SignedAt, want)
	}
}

// TestSignUsesCurrentTime: Sign's clock is time.Now.
func TestSignUsesCurrentTime(t *testing.T) {
	f := newSignedFixture(t)
	before := time.Now().Truncate(time.Second)
	env, err := Sign(f.sealed, f.signer)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	after := time.Now()
	v, err := Verify(env, f.trusted())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.SignedAt.Before(before) || v.SignedAt.After(after) {
		t.Fatalf("SignedAt %v not within [%v, %v]", v.SignedAt, before, after)
	}
}

func TestSignAtRefusesUnrepresentableTimes(t *testing.T) {
	f := newSignedFixture(t)
	for name, at := range map[string]time.Time{
		"zero":       {},
		"year 10000": time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		"year -1":    time.Date(-1, 12, 31, 23, 59, 59, 0, time.UTC),
		// Only the UTC year counts: 9999-12-31 23:30 at -01:00 is year 10000.
		"UTC rolls over": time.Date(9999, 12, 31, 23, 30, 0, 0, time.FixedZone("", -60*60)),
	} {
		t.Run(name, func(t *testing.T) {
			if env, err := SignAt(f.sealed, f.signer, at); !errors.Is(err, ErrSignedAtInvalid) || env != nil {
				t.Fatalf("SignAt: got %d bytes, %v, want ErrSignedAtInvalid", len(env), err)
			}
		})
	}
	for _, at := range []time.Time{time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)} {
		if _, err := SignAt(f.sealed, f.signer, at); err != nil {
			t.Fatalf("SignAt(%v), a representable edge: %v", at, err)
		}
	}
}

// TestVerifyAcceptsHandBuiltCanonicalLine is envelopeWithLine's control: a
// canonical line it builds verifies, so the refusals below are not
// vacuous.
func TestVerifyAcceptsHandBuiltCanonicalLine(t *testing.T) {
	f := newSignedFixture(t)
	v, err := Verify(envelopeWithLine(f, "signed-at 2030-01-02T03:04:05Z"), f.trusted())
	if err != nil || !v.SignedAt.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("Verify: %v, %v", v.SignedAt, err)
	}
}

// TestVerifyRefusesNonCanonicalSignedAt: every other spelling of a time,
// and every out-of-range field, is refused as malformed even when it is
// correctly signed, so an envelope has one accepted spelling.
func TestVerifyRefusesNonCanonicalSignedAt(t *testing.T) {
	f := newSignedFixture(t)
	for _, line := range []string{
		"signed-at 2026-09-24T10:50:51z",
		"signed-at 2026-09-24t10:50:51Z",
		"signed-at 2026-09-24 10:50:51Z",
		"signed-at 2026-09-24T10:50:51+00:00",
		"signed-at 2026-09-24T10:50:51.0Z",
		"signed-at 2026-09-24T10:50Z",
		"signed-at 2026-9-24T10:50:51Z",
		"signed-at 2026-13-24T10:50:51Z",
		"signed-at 2026-02-30T10:50:51Z",
		"signed-at 2026-09-24T24:00:00Z",
		"signed-at 2026-09-24T10:60:51Z",
		"signed-at 2016-12-31T23:59:60Z",
		"signed-at 0001-01-01T00:00:00Z", // the zero time.Time
		"signed-at +026-09-24T10:50:51Z",
		"signed-at  026-09-24T10:50:51Z",
		"signed-at 2026-09-24T10:50:51Z ",
		" signed-at 2026-09-24T10:50:51Z",
		"signed-at 2026-09-24T10:50:51Z\r",
		"signed-at\t2026-09-24T10:50:51Z",
		"Signed-at 2026-09-24T10:50:51Z",
		"signed_at 2026-09-24T10:50:51Z",
		"signed-at=2026-09-24T10:50:51Z",
		"signed-at 1790064651",
		"",
	} {
		t.Run(line, func(t *testing.T) {
			requireRefused(t, envelopeWithLine(f, line), f.trusted(), ErrEnvelopeMalformed)
		})
	}
}

// TestVerifyRefusesAlteredSignedAt: the signature covers the time, so a
// canonical but different signed-at line (a replayed envelope made to look
// fresher, or back-dated) fails the signature check.
func TestVerifyRefusesAlteredSignedAt(t *testing.T) {
	f := newSignedFixture(t)
	for _, stamp := range []string{"2026-09-24T10:50:52Z", "2026-09-24T10:50:50Z", "2099-01-01T00:00:00Z"} {
		env := bytes.Clone(f.env)
		copy(env[signedAtLineOff+len(signedAtPrefix):], stamp)
		requireRefused(t, env, f.trusted(), ErrSignatureInvalid)
	}
}
