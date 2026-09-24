package seal

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// smallOrderBlocklist is the published list of small-order Ed25519 point
// encodings (libsodium's ge25519_has_small_order blocklist, as discussed
// in "Taming the many EdDSAs"), each also with its sign bit set. It comes
// from outside this package's own arithmetic, so it cross-checks
// strongPublicKey rather than restating it.
var smallOrderBlocklist = []string{
	"0100000000000000000000000000000000000000000000000000000000000000", // identity, order 1
	"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // y = -1, order 2
	"0000000000000000000000000000000000000000000000000000000000000000", // y = 0, order 4
	"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", // order 8
	"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a", // order 8
	"edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // y = p (non-canonical 0)
	"eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // y = p+1 (non-canonical 1)
}

func TestStrongPublicKeyRefusesSmallOrderBlocklist(t *testing.T) {
	// The first five are canonical, on-curve points: they must be refused
	// for their ORDER, so the cofactor check is really what is exercised
	// (not merely the decoder refusing them).
	for _, h := range smallOrderBlocklist[:5] {
		key, _ := hex.DecodeString(h)
		if _, ok := decodeEdPoint(key); !ok {
			t.Errorf("%s does not decode as a canonical curve point", h)
		}
	}
	for _, h := range smallOrderBlocklist {
		for _, signBit := range []byte{0, 0x80} {
			key, err := hex.DecodeString(h)
			if err != nil {
				t.Fatal(err)
			}
			key[31] |= signBit
			if strongPublicKey(key) {
				t.Errorf("strongPublicKey accepted small-order encoding %x", key)
			}
		}
	}
}

func TestStrongPublicKeyRefusesNonCanonicalAndOffCurve(t *testing.T) {
	// y = p + 2 .. p + 18 are non-canonical spellings of y = 2 .. 18.
	for extra := byte(2); extra <= 18; extra++ {
		key, _ := hex.DecodeString("edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f")
		key[0] += extra
		if strongPublicKey(key) {
			t.Errorf("strongPublicKey accepted non-canonical y = p+%d", extra)
		}
	}
	// x = 0 with the sign bit set ("-0") is a non-canonical spelling of
	// the points with x = 0; the decoder itself must refuse it (the
	// small-order check would mask this, since both such points are weak).
	for _, h := range []string{
		"0100000000000000000000000000000000000000000000000000000000000080",
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	} {
		key, _ := hex.DecodeString(h)
		if _, ok := decodeEdPoint(key); ok {
			t.Errorf("decodeEdPoint accepted negative zero %s", h)
		}
	}
	// y = 2 is not on the curve (x^2 would be a non-square).
	offCurve := make([]byte, 32)
	offCurve[0] = 2
	for _, bad := range [][]byte{offCurve, nil, make([]byte, 31), make([]byte, 33)} {
		if strongPublicKey(bad) {
			t.Errorf("strongPublicKey accepted %x", bad)
		}
	}
}

// TestStrongPublicKeyAcceptsRealKeys: every key ed25519 generates, and
// the base point itself, is a strong key.
func TestStrongPublicKeyAcceptsRealKeys(t *testing.T) {
	base, _ := hex.DecodeString("5866666666666666666666666666666666666666666666666666666666666666")
	if !strongPublicKey(base) {
		t.Fatal("strongPublicKey refused the base point")
	}
	for range 64 {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if !strongPublicKey(pub) {
			t.Fatalf("strongPublicKey refused a generated key %x", []byte(pub))
		}
	}
}
