package seal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// genSigner returns a fresh Signer plus its signer-file line (the secret's
// only text form, which the refusal tests below grep every error for).
func genSigner(t testing.TB) (Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	line := signerSecretType + " " + keyEncoding.EncodeToString(priv.Seed())
	return Signer{key: priv}, line
}

// writeKeyFile writes content to dir/name with exactly perm (os.WriteFile's
// mode is narrowed by umask, so it is forced with Chmod) and returns the
// path.
func writeKeyFile(t *testing.T, dir, name, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod key file: %v", err)
	}
	return path
}

func TestLoadSignerAcceptsOwnedStrictFile(t *testing.T) {
	want, line := genSigner(t)
	path := writeKeyFile(t, t.TempDir(), "signer", "# my signer\n\n"+line+"\n", 0o600)

	got, err := LoadSigner(path)
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	if !bytes.Equal(got.key, want.key) {
		t.Fatal("LoadSigner returned a different key than the file holds")
	}
}

// TestLoadSignerRefusals covers every content-level refusal. Every case
// file also holds (or is) the real secret line, and no error may echo it.
func TestLoadSignerRefusals(t *testing.T) {
	_, secret := genSigner(t)
	_, other := genSigner(t)
	ageID, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	seed := strings.Fields(secret)[1]
	cases := []struct {
		name, content string
		want          error
	}{
		{"empty", "", ErrSignerCount},
		{"comments only", "# nothing\n\n", ErrSignerCount},
		{"two keys", secret + "\n" + other + "\n", ErrSignerCount},
		{"public line", signerPublicType + " " + seed + "\n", ErrSignerRefused},
		{"age identity", ageID.String() + "\n", ErrSignerRefused},
		{"bare seed", seed + "\n", ErrSignerRefused},
		{"missing key", signerSecretType + "\n", ErrSignerMalformed},
		{"extra field", secret + " label\n", ErrSignerMalformed},
		{"short key", signerSecretType + " " + seed[:len(seed)-1] + "\n", ErrSignerMalformed},
		{"padded key", secret + "=\n", ErrSignerMalformed},
		{"non-canonical key", nonCanonical(secret) + "\n", ErrSignerMalformed},
		{"too large", secret + "\n" + strings.Repeat("#", maxKeyFileBytes) + "\n", ErrKeyFileTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, t.TempDir(), "signer", tc.content, 0o600)
			_, err := LoadSigner(path)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), seed) {
				t.Fatalf("error echoes the signing key: %v", err)
			}
		})
	}
}

// nonCanonical returns line with its last base64 character changed so the
// value's unused trailing bits are non-zero: the same key bytes spelled a
// second way, which strict decoding must refuse.
func nonCanonical(line string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	last := strings.IndexByte(alphabet, line[len(line)-1])
	return line[:len(line)-1] + string(alphabet[last^1])
}

// hardeningCase is one file-system probe run against both loaders: the
// same probes task ce2 ran against the recipients file.
type hardeningCase struct {
	name  string
	setup func(t *testing.T, dir, content string) string // returns the path to load
}

func hardeningCases(perm os.FileMode) []hardeningCase {
	return []hardeningCase{
		{"symlinked file", func(t *testing.T, dir, content string) string {
			real := writeKeyFile(t, dir, "real", content, perm)
			return symlink(t, real, filepath.Join(dir, "link"))
		}},
		{"symlinked parent", func(t *testing.T, dir, content string) string {
			sub := filepath.Join(dir, "sub")
			mkdir(t, sub)
			writeKeyFile(t, sub, "key", content, perm)
			return filepath.Join(symlink(t, sub, filepath.Join(dir, "sublink")), "key")
		}},
		{"directory", func(t *testing.T, dir, _ string) string {
			sub := filepath.Join(dir, "key")
			mkdir(t, sub)
			return sub
		}},
		{"fifo", func(t *testing.T, dir, _ string) string {
			path := filepath.Join(dir, "key")
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatalf("mkfifo: %v", err)
			}
			return path
		}},
		{"missing", func(_ *testing.T, dir, _ string) string {
			return filepath.Join(dir, "missing")
		}},
	}
}

func symlink(t *testing.T, target, link string) string {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return link
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

// hardeningWant maps a hardeningCase name to the refusal a loader with the
// given sentinels must return.
func hardeningWant(name string, symlinkErr, notRegular error) error {
	switch name {
	case "symlinked file", "symlinked parent":
		return symlinkErr
	case "missing":
		return os.ErrNotExist
	default:
		return notRegular
	}
}

func TestLoadSignerHardening(t *testing.T) {
	_, secret := genSigner(t)
	for _, tc := range hardeningCases(0o600) {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t, t.TempDir(), secret+"\n")
			_, err := LoadSigner(path)
			want := hardeningWant(tc.name, ErrSignerSymlink, ErrSignerNotRegular)
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
}

// TestLoadSignerRefusesAnyGroupOrOtherBit proves the signer file gets the
// private-key rule (0o077), read bits included.
func TestLoadSignerRefusesAnyGroupOrOtherBit(t *testing.T) {
	_, secret := genSigner(t)
	for _, perm := range []os.FileMode{0o640, 0o604, 0o620, 0o602, 0o610, 0o601, 0o666} {
		t.Run(fmt.Sprintf("%04o", perm), func(t *testing.T) {
			path := writeKeyFile(t, t.TempDir(), "signer", secret+"\n", perm)
			_, err := LoadSigner(path)
			if !errors.Is(err, ErrSignerMode) {
				t.Fatalf("got %v, want ErrSignerMode", err)
			}
		})
	}
}

func TestSignerFileRefusesForeignOwner(t *testing.T) {
	me := unix.Geteuid()
	info := safepath.Info{Mode: unix.S_IFREG | 0o600, UID: uint32(me + 1)}
	if err := signerFile.checkOwnerAndMode(info, "/fake/signer", me); !errors.Is(err, ErrSignerNotOwned) {
		t.Fatalf("got %v, want ErrSignerNotOwned", err)
	}
}

// TestSignerFormatNeverPrintsPrivateKey proves no fmt verb leaks the
// private key of a Signer that ends up in a log line or error.
func TestSignerFormatNeverPrintsPrivateKey(t *testing.T) {
	s, secret := genSigner(t)
	seed := strings.Fields(secret)[1]
	forbidden := []string{seed, fmt.Sprintf("%x", []byte(s.key)), fmt.Sprintf("%v", []byte(s.key))}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%q", "%d"} {
		out := fmt.Sprintf(verb, s) + fmt.Sprintf(verb, &s) + fmt.Sprintf(verb, []Signer{s})
		for _, f := range forbidden {
			if strings.Contains(out, f) {
				t.Fatalf("%s prints private key material: %s", verb, out)
			}
		}
		if !strings.Contains(out, s.Public().String()) {
			t.Fatalf("%s does not print the public key: %s", verb, out)
		}
	}
	if got := fmt.Sprint(Signer{}); got != "seal.Signer(invalid)" {
		t.Fatalf("zero Signer prints %q", got)
	}
}

func TestLoadTrustedSignersAcceptsReadableFileWithLabels(t *testing.T) {
	a, _ := genSigner(t)
	b, _ := genSigner(t)
	wantA := a.Public()
	wantA.Label = "ci runner  one"
	content := "# fleet signers\n" + wantA.String() + "\n\n  " + b.Public().String() + "  \n"
	path := writeKeyFile(t, t.TempDir(), "signers", content, 0o644)

	got, err := LoadTrustedSigners(path)
	if err != nil {
		t.Fatalf("LoadTrustedSigners: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d signers, want 2", len(got))
	}
	if !bytes.Equal(got[0].Key, wantA.Key) || got[0].Label != "ci runner one" {
		t.Fatalf("first entry = %v, want key of a with label %q", got[0], "ci runner one")
	}
	if !bytes.Equal(got[1].Key, b.Public().Key) || got[1].Label != "" {
		t.Fatalf("second entry = %v, want key of b with no label", got[1])
	}
}

func TestLoadTrustedSignersRefusals(t *testing.T) {
	s, secret := genSigner(t)
	pub := s.Public().String()
	b64 := strings.Fields(pub)[1]
	ageID, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	cases := []struct {
		name, content string
		want          error
	}{
		{"empty", "", ErrNoTrustedSigners},
		{"comments only", "# none yet\n", ErrNoTrustedSigners},
		{"secret line", secret + "\n", ErrTrustedSignerRefused},
		{"ssh key", "ssh-ed25519 " + b64 + " me@host\n", ErrTrustedSignerRefused},
		{"age recipient", ageID.Recipient().String() + "\n", ErrTrustedSignerRefused},
		{"bare key", b64 + "\n", ErrTrustedSignerRefused},
		{"missing key", signerPublicType + "\n", ErrTrustedSignerMalformed},
		{"short key", signerPublicType + " " + b64[:len(b64)-2] + "\n", ErrTrustedSignerMalformed},
		{"seed-length key", signerPublicType + " " + b64 + "AAAA\n", ErrTrustedSignerMalformed},
		{"padded key", pub + "=\n", ErrTrustedSignerMalformed},
		{"non-canonical key", nonCanonical(pub) + "\n", ErrTrustedSignerMalformed},
		{"escape in label", pub + " evil\x1b[2Jlabel\n", ErrTrustedSignerMalformed},
		{"invalid UTF-8 label", pub + " \xff\n", ErrTrustedSignerMalformed},
		{"one bad line among good", pub + "\n" + signerPublicType + " !!\n", ErrTrustedSignerMalformed},
		{"too large", pub + "\n" + strings.Repeat("#", maxKeyFileBytes) + "\n", ErrKeyFileTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, t.TempDir(), "signers", tc.content, 0o644)
			_, err := LoadTrustedSigners(path)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), strings.Fields(secret)[1]) {
				t.Fatalf("error echoes a signing key: %v", err)
			}
		})
	}
}

// TestLoadTrustedSignersHardening reproduces task ce2's probes against the
// trusted-signers file from day one (docs/plan-signing.md threat S4).
func TestLoadTrustedSignersHardening(t *testing.T) {
	s, _ := genSigner(t)
	for _, tc := range hardeningCases(0o644) {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t, t.TempDir(), s.Public().String()+"\n")
			_, err := LoadTrustedSigners(path)
			want := hardeningWant(tc.name, ErrTrustedSignersSymlink, ErrTrustedSignersNotRegular)
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
}

// TestLoadTrustedSignersWriteBitsOnly proves the public-key rule (0o022):
// group/other read is fine, group/other write is refused.
func TestLoadTrustedSignersWriteBitsOnly(t *testing.T) {
	s, _ := genSigner(t)
	for perm, refused := range map[os.FileMode]bool{
		0o600: false, 0o644: false, 0o444: false, 0o664: true, 0o666: true, 0o602: true, 0o620: true,
	} {
		t.Run(fmt.Sprintf("%04o", perm), func(t *testing.T) {
			path := writeKeyFile(t, t.TempDir(), "signers", s.Public().String()+"\n", perm)
			_, err := LoadTrustedSigners(path)
			if refused != errors.Is(err, ErrTrustedSignersWritable) || (!refused && err != nil) {
				t.Fatalf("mode %04o: got %v, want refused=%v", perm, err, refused)
			}
		})
	}
}

func TestTrustedSignersFileRefusesForeignOwner(t *testing.T) {
	me := unix.Geteuid()
	info := safepath.Info{Mode: unix.S_IFREG | 0o644, UID: uint32(me + 1)}
	err := trustedSignersFile.checkOwnerAndMode(info, "/fake/signers", me)
	if !errors.Is(err, ErrTrustedSignersNotOwned) {
		t.Fatalf("got %v, want ErrTrustedSignersNotOwned", err)
	}
}

// TestLoadedKeysSignAndVerify is the file-level round trip: a signer and a
// trusted-signers file written the way an operator would (the public line
// copied verbatim from Public().String()) sign and verify a sealed plan.
func TestLoadedKeysSignAndVerify(t *testing.T) {
	s, secret := genSigner(t)
	dir := t.TempDir()
	signer, err := LoadSigner(writeKeyFile(t, dir, "signer", secret+"\n", 0o600))
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	pub := s.Public()
	pub.Label = "laptop"
	trusted, err := LoadTrustedSigners(writeKeyFile(t, dir, "signers", pub.String()+"\n", 0o644))
	if err != nil {
		t.Fatalf("LoadTrustedSigners: %v", err)
	}
	recipient, _ := genKeyPair(t)
	sealed := sealBytes(t, "plan", []Recipient{recipient})
	env, err := Sign(sealed, signer)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, who, err := Verify(env, trusted)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !bytes.Equal(got, sealed) || who.Label != "laptop" {
		t.Fatalf("Verify returned label %q and %d bytes, want laptop and the sealed bytes", who.Label, len(got))
	}
}
