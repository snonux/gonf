package seal

import (
	"crypto/ed25519"
	"math/big"
)

// This file rejects weak Ed25519 public keys (task 6g2 review). Go's
// ed25519.Verify accepts a public key of small order: for the identity
// point (01 00..00) the signature R=identity, S=0 verifies EVERY message,
// and for the all-zero encoding (an order-4 point, and exactly what an
// operator might paste as a placeholder) a random R=[S]B verifies about
// one try in four. Either would let anyone, with no private key at all,
// forge a plan signature "by" that trusted signer. So a key is accepted
// only when it is the canonical encoding of a curve point whose
// cofactor multiple [8]A is not the identity — the check "Taming the
// many EdDSAs" (Chalkias et al., 2020) recommends, which rejects all
// eight small-order points and every non-canonical encoding of any point
// (Go's own decoder accepts the 19 non-canonical y values p..p+18).
// Mixed-order keys (a prime-order point plus a small-order one) pass:
// forging for one still needs the prime-order part's discrete logarithm.
//
// The arithmetic uses math/big rather than filippo.io/edwards25519, which
// is not otherwise a dependency of this module. Speed is irrelevant here:
// it runs once per key loaded or matched, never per byte of a plan.

var (
	// edP is the field prime 2^255 - 19.
	edP = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	// edD is the twisted Edwards curve constant d = -121665/121666 mod p.
	edD = func() *big.Int {
		d := new(big.Int).ModInverse(big.NewInt(121666), edP)
		d.Mul(d, big.NewInt(-121665))
		return d.Mod(d, edP)
	}()
)

// edPoint is an affine point (x, y) on edwards25519: -x^2 + y^2 = 1 + d x^2 y^2.
type edPoint struct{ x, y *big.Int }

// strongPublicKey reports whether pub is a canonical Ed25519 public key
// encoding of a point that is not of small order (see the file comment).
func strongPublicKey(pub []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	pt, ok := decodeEdPoint(pub)
	if !ok {
		return false
	}
	for range 3 { // [8]P = P doubled three times
		pt = edAdd(pt, pt)
	}
	isIdentity := pt.x.Sign() == 0 && pt.y.Cmp(big.NewInt(1)) == 0
	return !isIdentity
}

// decodeEdPoint decodes enc (RFC 8032 section 5.1.3) strictly: y must be
// below p, the point must be on the curve, and x = 0 must not carry a set
// sign bit (the non-canonical "-0").
func decodeEdPoint(enc []byte) (edPoint, bool) {
	le := make([]byte, len(enc))
	copy(le, enc)
	sign := le[31] >> 7
	le[31] &= 0x7f
	y := new(big.Int).SetBytes(reversed(le))
	if y.Cmp(edP) >= 0 {
		return edPoint{}, false
	}
	// x^2 = (y^2 - 1) / (d y^2 + 1)
	y2 := new(big.Int).Mul(y, y)
	u := new(big.Int).Sub(y2, big.NewInt(1))
	v := new(big.Int).Mul(edD, y2)
	v.Add(v, big.NewInt(1)).Mod(v, edP)
	x2 := u.Mul(u, new(big.Int).ModInverse(v, edP))
	x2.Mod(x2, edP)
	x := new(big.Int).ModSqrt(x2, edP)
	if x == nil {
		return edPoint{}, false
	}
	if x.Sign() == 0 && sign == 1 {
		return edPoint{}, false
	}
	if uint8(x.Bit(0)) != sign {
		x.Sub(edP, x)
	}
	return edPoint{x: x, y: y}, true
}

// edAdd returns a + b with the complete twisted Edwards addition law
// (a = -1; d is not a square mod p, so the denominators are never zero).
func edAdd(a, b edPoint) edPoint {
	x1y2 := new(big.Int).Mul(a.x, b.y)
	y1x2 := new(big.Int).Mul(a.y, b.x)
	x1x2 := new(big.Int).Mul(a.x, b.x)
	y1y2 := new(big.Int).Mul(a.y, b.y)
	t := new(big.Int).Mul(edD, x1x2)
	t.Mul(t, y1y2).Mod(t, edP)
	one := big.NewInt(1)
	xDen := new(big.Int).Add(one, t)
	yDen := new(big.Int).Sub(one, t)
	x := x1y2.Add(x1y2, y1x2)
	x.Mul(x, xDen.ModInverse(xDen.Mod(xDen, edP), edP)).Mod(x, edP)
	y := y1y2.Add(y1y2, x1x2)
	y.Mul(y, yDen.ModInverse(yDen.Mod(yDen, edP), edP)).Mod(y, edP)
	return edPoint{x: x, y: y}
}

// reversed returns a copy of b in reverse order (little- to big-endian).
func reversed(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[len(b)-1-i] = c
	}
	return out
}
