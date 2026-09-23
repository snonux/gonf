package seal

import (
	"testing"

	"filippo.io/age"
)

// genKeyPair returns a fresh hybrid recipient/identity pair, wrapped in this
// package's own Recipient and Identity types (test files are in-package, so
// they can set the unexported inner field directly instead of round-tripping
// through the Bech32 text encoding ParseRecipients/LoadIdentities parse).
func genKeyPair(t testing.TB) (Recipient, Identity) {
	t.Helper()
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatalf("generate hybrid identity: %v", err)
	}
	return Recipient{inner: id.Recipient()}, Identity{inner: id}
}

// flipOneChar returns s with exactly one character changed, deterministically
// (never a no-op, unlike picking a "different" byte at random and risking it
// matching by chance), so a test that needs "the same string but corrupted"
// never accidentally leaves it identical.
func flipOneChar(s string) string {
	b := []byte(s)
	mid := len(b) / 2
	if b[mid] == 'q' {
		b[mid] = 'p'
	} else {
		b[mid] = 'q'
	}
	return string(b)
}
