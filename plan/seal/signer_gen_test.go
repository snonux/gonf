package seal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for GenerateSigner and WriteSignerFile (signer_gen.go, task 7g2).

func TestGenerateSignerIsRandomAndValid(t *testing.T) {
	a, err := GenerateSigner()
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}
	b, err := GenerateSigner()
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}
	if !a.valid() || !b.valid() || bytes.Equal(a.Public().Key, b.Public().Key) {
		t.Fatal("GenerateSigner returned an invalid or repeated key")
	}
	if !strongPublicKey(a.Public().Key) {
		t.Fatal("generated key is not a strong public key")
	}
}

// TestWriteSignerFileRoundTrip: the written file is 0600 and LoadSigner
// reads back the same key, which then signs what its public line verifies.
func TestWriteSignerFileRoundTrip(t *testing.T) {
	s, err := GenerateSigner()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "signer")
	if err := WriteSignerFile(path, s); err != nil {
		t.Fatalf("WriteSignerFile: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != 0o600 {
		t.Fatalf("signer file mode %v, %v; want a regular 0600 file", info.Mode(), err)
	}
	loaded, err := LoadSigner(path)
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	if !bytes.Equal(loaded.Public().Key, s.Public().Key) {
		t.Fatal("LoadSigner read back a different key")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# public key: "+s.Public().String()+"\n") {
		t.Fatalf("signer file does not name its public key in a comment:\n%s", data)
	}
}

// TestWriteSignerFileNeverReplaces: an existing file, or a symlink at the
// path (dangling or not), is refused and left exactly as it was.
func TestWriteSignerFileNeverReplaces(t *testing.T) {
	s, _ := GenerateSigner()
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("old key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{existing, link, dangling} {
		if err := WriteSignerFile(p, s); !errors.Is(err, ErrSignerFileExists) {
			t.Fatalf("WriteSignerFile(%s): got %v, want ErrSignerFileExists", p, err)
		}
	}
	if data, _ := os.ReadFile(existing); string(data) != "old key\n" {
		t.Fatalf("existing file changed: %q", data)
	}
	for _, p := range []string{target, filepath.Join(dir, "nowhere")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("a symlink was followed: %s exists (%v)", p, err)
		}
	}
}

// TestWriteSignerFileRefusesSymlinkedParent: a symlinked directory above
// the file is refused, as LoadSigner would refuse to read it.
func TestWriteSignerFileRefusesSymlinkedParent(t *testing.T) {
	s, _ := GenerateSigner()
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "via")); err != nil {
		t.Fatal(err)
	}
	if err := WriteSignerFile(filepath.Join(dir, "via", "signer"), s); !errors.Is(err, ErrSignerSymlink) {
		t.Fatalf("got %v, want ErrSignerSymlink", err)
	}
	if _, err := os.Lstat(filepath.Join(real, "signer")); !os.IsNotExist(err) {
		t.Fatalf("signer file written through the symlinked parent (%v)", err)
	}
}

func TestWriteSignerFileRefusals(t *testing.T) {
	s, _ := GenerateSigner()
	dir := t.TempDir()
	if err := WriteSignerFile(filepath.Join(dir, "missing", "signer"), s); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent: got %v, want os.ErrNotExist", err)
	}
	if err := WriteSignerFile(filepath.Join(dir, "zero"), Signer{}); !errors.Is(err, ErrSignerInvalid) {
		t.Fatalf("zero Signer: got %v, want ErrSignerInvalid", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "zero")); !os.IsNotExist(err) {
		t.Fatal("a zero Signer still created a file")
	}
	if err := WriteSignerFile("/", s); err == nil {
		t.Fatal("WriteSignerFile(/) succeeded")
	}
}

// TestWriteSignerFileErrorsNameNoKeyMaterial: a refusal names the path and
// class only, never the key it was asked to write.
func TestWriteSignerFileErrorsNameNoKeyMaterial(t *testing.T) {
	s, _ := GenerateSigner()
	path := filepath.Join(t.TempDir(), "signer")
	if err := WriteSignerFile(path, s); err != nil {
		t.Fatal(err)
	}
	err := WriteSignerFile(path, s)
	seed := keyEncoding.EncodeToString(s.privateKey().Seed())
	pub := keyEncoding.EncodeToString(s.Public().Key)
	if err == nil || strings.Contains(err.Error(), seed) || strings.Contains(err.Error(), pub) {
		t.Fatalf("error names key material or is nil: %v", err)
	}
}
