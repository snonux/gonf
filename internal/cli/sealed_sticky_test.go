package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// Destination-side tests for sealed sticky-dir refs (task 0g2). Each test
// stages a push the way internal/remote does (sealed refs uploaded through
// the real "gonf apply -apply-dir <sticky> -" upload session), then applies
// the elevated chunk's keyed GONF-PUSH/2 frame through the real CLI.
// internal/cli's end-to-end test (sealed_sticky_e2e_test.go) drives the
// real controller instead.

// The fixture's secrets: never allowed in the sticky dir, stderr or logs.
const (
	stickySecret  = "s3cr3t-sealed-content"
	stickySecret2 = "an0ther-sealed-secret"
	stickyPlain   = "plain-shared-content"
)

// sealedStickyFixture is one staged sealed push: the sticky dir after the
// upload session, the elevated chunk's ops, the push's key, and the files
// the chunk writes.
type sealedStickyFixture struct {
	sticky, stagingRoot string
	ops                 []plan.Op
	id                  seal.Identity
	recipient           seal.Recipient
	keyLine             string
	secretRef, plainRef string
	secret2Ref          string
	targets             []string // sealed op, plain op, second sealed op
	mem                 *plan.MemoryStore
}

// newSealedStickyFixture stages a push whose elevated chunk holds two
// sensitive blob ops (sealed refs) and one non-sensitive blob op (a plain
// ref that stays in the sticky dir), and uploads it into a fresh sticky dir.
func newSealedStickyFixture(t *testing.T) *sealedStickyFixture {
	t.Helper()
	f := &sealedStickyFixture{stagingRoot: isolateSealedStagingRoot(t), mem: plan.NewMemoryStore()}
	root := t.TempDir()
	f.sticky = filepath.Join(root, "sticky")
	f.secretRef = writeMemBlob(t, f.mem, "secret.conf", stickySecret)
	f.plainRef = writeMemBlob(t, f.mem, "plain.conf", stickyPlain)
	f.secret2Ref = writeMemBlob(t, f.mem, "secret2.conf", stickySecret2)
	f.targets = []string{filepath.Join(root, "out-secret"), filepath.Join(root, "out-plain"), filepath.Join(root, "out-secret2")}
	f.ops = []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "sealed-sticky"},
		{Op: plan.KindFile, Path: f.targets[0], Mode: "0600", Blob: f.secretRef, Sensitive: true, Elevate: true},
		{Op: plan.KindFile, Path: f.targets[1], Mode: "0600", Blob: f.plainRef, Elevate: true},
		{Op: plan.KindFile, Path: f.targets[2], Mode: "0600", Blob: f.secret2Ref, Sensitive: true, Elevate: true},
	}
	var err error
	if f.id, f.recipient, err = seal.GenerateEphemeral(); err != nil {
		t.Fatal(err)
	}
	if f.keyLine, err = seal.EncodeEphemeral(f.id); err != nil {
		t.Fatal(err)
	}
	f.upload(t, map[string][]byte{
		f.secretRef:  sealRefArchive(t, f.mem, f.secretRef, f.recipient),
		f.secret2Ref: sealRefArchive(t, f.mem, f.secret2Ref, f.recipient),
	})
	return f
}

// writeMemBlob stores content as a file blob and returns its ref.
func writeMemBlob(t *testing.T, mem *plan.MemoryStore, name, content string) string {
	t.Helper()
	ref, err := mem.WriteFile(name, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// sealRefArchive is the controller's sealRef: ref's archive sealed to r.
func sealRefArchive(t *testing.T, mem plan.BlobReader, ref string, r seal.Recipient) []byte {
	t.Helper()
	var arch bytes.Buffer
	if err := plan.WriteRefArchive(&arch, mem, ref); err != nil {
		t.Fatal(err)
	}
	return sealBytes(t, arch.Bytes(), r)
}

// sealBytes seals plaintext to r as one age stream.
func sealBytes(t *testing.T, plaintext []byte, r seal.Recipient) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := seal.Seal(&out, []seal.Recipient{r})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// upload runs the push's blob-upload session into f.sticky: the header-only
// GONF-PUSH/1 frame carrying mem's blobs with sealed replacing their refs.
func (f *sealedStickyFixture) upload(t *testing.T, sealed map[string][]byte) {
	t.Helper()
	var frame bytes.Buffer
	if err := plan.EncodePush(&frame, f.ops[:1], plan.WithSealedRefs(f.mem, sealed)); err != nil {
		t.Fatal(err)
	}
	if code, stderr := f.apply(t, frame.Bytes(), true); code != 0 {
		t.Fatalf("upload session exit %d: %s", code, stderr)
	}
	assertNoStickyPlaintext(t, f.sticky)
}

// keyedFrame is the elevated chunk's GONF-PUSH/2 frame for ops with keyLine.
func keyedFrame(t *testing.T, ops []plan.Op, keyLine string) []byte {
	t.Helper()
	key, err := plan.NewPushKey(keyLine)
	if err != nil {
		t.Fatal(err)
	}
	var frame bytes.Buffer
	if err := plan.EncodePushWithKey(&frame, ops, nil, key); err != nil {
		t.Fatal(err)
	}
	return frame.Bytes()
}

// apply runs "gonf apply [-apply-dir sticky] -" on frame and returns its
// exit code and stderr plus captured log output.
func (f *sealedStickyFixture) apply(t *testing.T, frame []byte, sticky bool) (int, string) {
	t.Helper()
	logs := testutil.CaptureLog(t, logger.LevelDebug)
	withStdinBytes(t, frame)
	args := []string{"apply"}
	if sticky {
		args = append(args, "-apply-dir", f.sticky)
	}
	code, stderr := runGonf(t, append(args, "-")...)
	return code, stderr + logs()
}

// applyChunk applies the keyed elevated chunk and returns exit code and output.
func (f *sealedStickyFixture) applyChunk(t *testing.T) (int, string) {
	t.Helper()
	return f.apply(t, keyedFrame(t, f.ops, f.keyLine), true)
}

// sealedPath is ref's sealed stream in the sticky dir.
func (f *sealedStickyFixture) sealedPath(ref string) string {
	return filepath.Join(f.sticky, filepath.FromSlash(plan.SealedBlobPath(ref)))
}

// requireRefused checks a refused chunk: exit 1, wantMsg on stderr, the op
// named by position, no op applied (not even the plain one), no leftover
// private run dir, and no ref name, secret or key echoed.
func (f *sealedStickyFixture) requireRefused(t *testing.T, code int, out, wantMsg string) {
	t.Helper()
	if code != 1 || !strings.Contains(out, wantMsg) {
		t.Fatalf("apply exit %d, output %q; want exit 1 and %q", code, out, wantMsg)
	}
	for _, target := range f.targets {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("refused chunk still applied %s: %v", filepath.Base(target), err)
		}
	}
	f.requireClean(t, out)
}

// requireClean checks the invariants of every outcome: no private run dir
// left, no plaintext in the sticky dir, no ref name, secret or key in out.
func (f *sealedStickyFixture) requireClean(t *testing.T, out string) {
	t.Helper()
	requireNoLeftoverSealedRunDirs(t, f.stagingRoot)
	assertNoStickyPlaintext(t, f.sticky)
	for _, leak := range []string{f.keyLine, "AGE-SECRET", "secret.conf", "secret2.conf", stickySecret, stickySecret2} {
		if strings.Contains(out, leak) {
			t.Fatalf("output leaks %q: %q", leak[:min(len(leak), 12)], out)
		}
	}
}

// assertNoStickyPlaintext fails when any file below sticky holds a secret.
func assertNoStickyPlaintext(t *testing.T, sticky string) {
	t.Helper()
	_ = filepath.WalkDir(sticky, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		for _, secret := range []string{stickySecret, stickySecret2} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("sticky dir file %s holds sealed plaintext", path)
			}
		}
		return nil
	})
}

// requireContent fails unless path holds want.
func requireContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q", filepath.Base(path), got, err, want)
	}
}

// The keyed chunk decrypts its sealed refs into a private run dir and
// applies them, while its plain ref still comes from the sticky dir; the
// sticky dir never holds plaintext, the run dir is gone afterwards, and no
// output carries the key or a secret.
func TestSealedStickyChunkAppliesFromPrivateRunDir(t *testing.T) {
	f := newSealedStickyFixture(t)
	code, out := f.applyChunk(t)
	if code != 0 {
		t.Fatalf("keyed chunk exit %d: %s", code, out)
	}
	requireContent(t, f.targets[0], stickySecret)
	requireContent(t, f.targets[1], stickyPlain)
	requireContent(t, f.targets[2], stickySecret2)
	f.requireClean(t, out)
	if _, err := os.Stat(filepath.Join(f.sticky, filepath.FromSlash(f.secretRef))); !os.IsNotExist(err) {
		t.Fatalf("sealed ref's plaintext appeared in the sticky dir: %v", err)
	}
}

// A sealed stream with one flipped byte (after the age header, in the
// payload) does not decrypt: refused by op position, nothing applied.
func TestSealedStickyTamperedRefRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	data, err := os.ReadFile(f.sealedPath(f.secret2Ref))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-5] ^= 0x01
	if err := os.WriteFile(f.sealedPath(f.secret2Ref), data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := f.applyChunk(t)
	f.requireRefused(t, code, out, "sealed sticky ref of op 3")
}

// A truncated sealed stream is refused: the extractor reads the age stream
// to its authenticated end.
func TestSealedStickyTruncatedRefRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	data, err := os.ReadFile(f.sealedPath(f.secretRef))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.sealedPath(f.secretRef), data[:len(data)-17], 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := f.applyChunk(t)
	f.requireRefused(t, code, out, "sealed sticky ref of op 1")
}

// A missing sealed stream is refused by op position, nothing applied.
func TestSealedStickyMissingRefRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	if err := os.Remove(f.sealedPath(f.secret2Ref)); err != nil {
		t.Fatal(err)
	}
	code, out := f.applyChunk(t)
	f.requireRefused(t, code, out, "sealed sticky ref of op 3: "+errSealedRefMissing.Error())
}

// Another ref's sealed stream swapped in under this ref's path decrypts
// (same push key) but holds members outside this ref: refused.
func TestSealedStickySwappedRefRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	other, err := os.ReadFile(f.sealedPath(f.secret2Ref))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.sealedPath(f.secretRef), other, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := f.applyChunk(t)
	f.requireRefused(t, code, out, "outside its ref")
}

// An archive sealed to the push key that carries an extra member besides
// its ref is refused before anything applies.
func TestSealedStickyExtraMemberRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	arch := gzipTar(t, map[string]string{f.secretRef: stickySecret, "blobs/extra.conf": "extra"})
	if err := os.WriteFile(f.sealedPath(f.secretRef), sealBytes(t, arch, f.recipient), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := f.applyChunk(t)
	f.requireRefused(t, code, out, "outside its ref")
}

// gzipTar returns a gzip-compressed tar of regular files name -> content,
// in sorted name order.
func gzipTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(files[name])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A symlink in place of a sealed stream (the login user owns the sticky
// dir) is refused, even when it points at a valid stream.
func TestSealedStickySymlinkedRefRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "valid.age")
	data, err := os.ReadFile(f.sealedPath(f.secretRef))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elsewhere, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.sealedPath(f.secretRef)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, f.sealedPath(f.secretRef)); err != nil {
		t.Fatal(err)
	}
	code, out := f.applyChunk(t)
	f.requireRefused(t, code, out, errSealedRefNotRegular.Error())
}

// A different push's key does not open this push's sealed refs.
func TestSealedStickyWrongKeyRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	other, _, err := seal.GenerateEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	line, err := seal.EncodeEphemeral(other)
	if err != nil {
		t.Fatal(err)
	}
	code, out := f.apply(t, keyedFrame(t, f.ops, line), true)
	f.requireRefused(t, code, out, errSealedRefDecrypt.Error())
	if strings.Contains(out, line) {
		t.Fatal("refusal echoes the wrong key")
	}
}

// A key line that frames correctly but is not a valid identity is refused
// without echoing it.
func TestSealedStickyMalformedKeyRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	bad := "AGE-SECRET-KEY-PQ-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	code, out := f.apply(t, keyedFrame(t, f.ops, bad), true)
	f.requireRefused(t, code, out, errMalformedStickyKey.Error())
	if strings.Contains(out, bad) {
		t.Fatal("refusal echoes the malformed key")
	}
}

// A keyed frame whose ops read no sealed ref is refused: the controller
// sends the key only to a chunk that needs it.
func TestSealedStickyKeyWithoutSealedOpRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	ops := []plan.Op{f.ops[0], f.ops[2]} // only the plain op
	code, out := f.apply(t, keyedFrame(t, ops, f.keyLine), true)
	f.requireRefused(t, code, out, errKeyWithoutSealedOp.Error())
}

// A keyless frame whose op reads a ref the sticky dir holds sealed is
// refused (a sealed op without a key), naming the op by position.
func TestSealedStickySealedOpWithoutKeyRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	var frame bytes.Buffer
	if err := plan.EncodePush(&frame, f.ops, nil); err != nil {
		t.Fatal(err)
	}
	code, out := f.apply(t, frame.Bytes(), true)
	f.requireRefused(t, code, out, "op 1 "+errSealedRefWithoutKey.Error())
}

// A keyed frame that embeds blobs is refused before anything is extracted:
// the sticky dir is not wiped and nothing applies.
func TestSealedStickyKeyedFrameWithBlobsRefused(t *testing.T) {
	f := newSealedStickyFixture(t)
	key, err := plan.NewPushKey(f.keyLine)
	if err != nil {
		t.Fatal(err)
	}
	var frame bytes.Buffer
	if err := plan.EncodePushWithKey(&frame, f.ops, f.mem, key); err != nil {
		t.Fatal(err)
	}
	code, out := f.apply(t, frame.Bytes(), true)
	f.requireRefused(t, code, out, errKeyedFrameWithBlobs.Error())
	if _, err := os.Stat(f.sealedPath(f.secretRef)); err != nil {
		t.Fatalf("refused keyed frame wiped the sticky dir: %v", err)
	}
}

// An op failing after the sealed refs were decrypted still removes the
// private run dir: its cleanup is deferred on every path.
func TestSealedStickyApplyFailureRemovesRunDir(t *testing.T) {
	f := newSealedStickyFixture(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.ops[3].Path = filepath.Join(blocker, "below-a-file") // cannot be created
	code, out := f.applyChunk(t)
	if code != 1 {
		t.Fatalf("failing chunk exit %d: %s", code, out)
	}
	f.requireClean(t, out)
}

// A plan-only chunk session must not wipe the blobs the upload session
// staged for it: every chunk after the upload reads them from the sticky
// dir. (Every session used to wipe it, which broke every multi-chunk push
// with blobs with "missing blob".)
func TestStickyChunkSessionKeepsUploadedBlobs(t *testing.T) {
	f := newSealedStickyFixture(t)
	ops := []plan.Op{f.ops[0], f.ops[2]} // the plain op alone, keyless
	var frame bytes.Buffer
	if err := plan.EncodePush(&frame, ops, nil); err != nil {
		t.Fatal(err)
	}
	if code, out := f.apply(t, frame.Bytes(), true); code != 0 {
		t.Fatalf("plain chunk exit %d: %s", code, out)
	}
	requireContent(t, f.targets[1], stickyPlain)
	if _, err := os.Stat(f.sealedPath(f.secretRef)); err != nil {
		t.Fatalf("plain chunk wiped the sealed refs of a later chunk: %v", err)
	}
	if code, out := f.applyChunk(t); code != 0 {
		t.Fatalf("keyed chunk after the plain one exit %d: %s", code, out)
	}
	requireContent(t, f.targets[0], stickySecret)
}
