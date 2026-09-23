package seal

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"filippo.io/age"
)

// sealBytes seals plaintext to recipients and returns the sealed bytes.
func sealBytes(t *testing.T, plaintext string, recipients []Recipient) []byte {
	t.Helper()
	var buf bytes.Buffer
	wc, err := Seal(&buf, recipients)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := io.WriteString(wc, plaintext); err != nil {
		t.Fatalf("write plaintext: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("close sealed writer: %v", err)
	}
	return buf.Bytes()
}

func TestSealOpenRoundTrip(t *testing.T) {
	recipient, identity := genKeyPair(t)
	const plaintext = "GONF-PUSH/1\nblobs 0\nplan\n"

	sealed := sealBytes(t, plaintext, []Recipient{recipient})

	r, err := Open(bytes.NewReader(sealed), []Identity{identity})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read opened plaintext: %v", err)
	}
	if string(got) != plaintext {
		t.Fatalf("round trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestSealOpenMultiRecipientRoundTrip(t *testing.T) {
	const plaintext = "one plan, several destinations"
	r1, i1 := genKeyPair(t)
	r2, i2 := genKeyPair(t)
	r3, i3 := genKeyPair(t)

	sealed := sealBytes(t, plaintext, []Recipient{r1, r2, r3})

	for name, id := range map[string]Identity{"first": i1, "second": i2, "third": i3} {
		t.Run(name, func(t *testing.T) {
			r, err := Open(bytes.NewReader(sealed), []Identity{id})
			if err != nil {
				t.Fatalf("Open with %s identity: %v", name, err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(got) != plaintext {
				t.Fatalf("mismatch for %s: got %q, want %q", name, got, plaintext)
			}
		})
	}
}

func TestOpenWrongIdentityFails(t *testing.T) {
	recipient, _ := genKeyPair(t)
	_, wrongIdentity := genKeyPair(t)

	sealed := sealBytes(t, "secret", []Recipient{recipient})

	r, err := Open(bytes.NewReader(sealed), []Identity{wrongIdentity})
	if err == nil {
		// Decrypt succeeding without a matching identity would be a total
		// break; also drain to be sure nothing readable came back.
		got, _ := io.ReadAll(r)
		t.Fatalf("Open with wrong identity succeeded, got plaintext %q", got)
	}
	var noMatch *age.NoIdentityMatchError
	if !errors.As(err, &noMatch) {
		t.Fatalf("Open with wrong identity: got %v, want a wrapped *age.NoIdentityMatchError", err)
	}
}

func TestSealRefusesZeroRecipients(t *testing.T) {
	var buf bytes.Buffer
	_, err := Seal(&buf, nil)
	if !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("Seal(nil recipients): got %v, want ErrNoRecipients", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("Seal(nil recipients) wrote %d bytes before refusing", buf.Len())
	}
}

func TestOpenRefusesZeroIdentities(t *testing.T) {
	recipient, _ := genKeyPair(t)
	sealed := sealBytes(t, "x", []Recipient{recipient})

	_, err := Open(bytes.NewReader(sealed), nil)
	if !errors.Is(err, ErrNoIdentities) {
		t.Fatalf("Open(nil identities): got %v, want ErrNoIdentities", err)
	}
}

// TestSealNeverMixesRecipientTypes proves, at the age library level, that
// the "age1pq only" policy in recipients.go is not merely cosmetic: it is
// the only thing standing between gonf and age's own "incompatible
// recipients" refusal, which age.Encrypt still enforces on the pinned age
// version (v1.3.2) when handed a classic X25519 recipient alongside a
// hybrid one. This test bypasses ParseRecipients on purpose (constructing
// an age.X25519Recipient directly, which gonf's own API can never produce)
// to check that underlying behavior is still present; it does not exercise
// any gonf-reachable path, since ParseRecipients refuses a classic
// recipient before Seal would ever see one (see TestParseRecipients in
// recipients_test.go).
func TestSealNeverMixesRecipientTypes(t *testing.T) {
	classicID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate X25519 identity: %v", err)
	}
	hybridID, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatalf("generate hybrid identity: %v", err)
	}

	var buf bytes.Buffer
	_, err = age.Encrypt(&buf, classicID.Recipient(), hybridID.Recipient())
	if err == nil {
		t.Fatalf("age.Encrypt accepted a mixed classic+hybrid recipient list on the pinned age version; the design's age1pq-only policy assumption no longer holds")
	}
	if !strings.Contains(err.Error(), "incompatible recipients") {
		t.Fatalf("age.Encrypt mixed-recipient error changed shape: %v (want it to mention \"incompatible recipients\")", err)
	}
}
