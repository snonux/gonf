package plan

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestEncodeDecodePushNoBlobs(t *testing.T) {
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/x", ContentB64: "eA=="},
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, nil); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ops) != 2 || got.Ops[0].ID != "p" || got.PlanDir != "" {
		t.Fatalf("%#v", got)
	}
}

func TestEncodeDecodePushWithMemoryBlobs(t *testing.T) {
	mem := NewMemoryStore()
	ref, err := mem.WriteFile("secret", []byte("sekrit\n"))
	if err != nil {
		t.Fatal(err)
	}
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/secret", Blob: ref},
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, mem); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(&buf, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanDir != dir {
		t.Fatalf("planDir=%q", got.PlanDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sekrit\n" {
		t.Fatalf("blob data %q", data)
	}
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(ref)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %#o", info.Mode().Perm())
	}
}

// fakeBlobReader is a minimal BlobReader that is not *MemoryStore, proving
// EncodePush depends only on the interface (DIP): the sole write path
// (WriteFile/WriteTree/WriteGlob) stays MemoryStore/Store-specific, but the
// push-time read-back works against any BlobReader implementation.
type fakeBlobReader struct {
	files map[string][]byte
}

func (f *fakeBlobReader) HasBlobs() bool { return len(f.files) > 0 }
func (f *fakeBlobReader) Refs() []string {
	refs := make([]string, 0, len(f.files))
	for ref := range f.files {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}
func (f *fakeBlobReader) FileBlob(ref string) ([]byte, bool) {
	data, ok := f.files[ref]
	return data, ok
}
func (f *fakeBlobReader) TreeBlob(ref string) ([]BlobEntry, bool) { return nil, false }

var _ BlobReader = (*fakeBlobReader)(nil)

func TestEncodeDecodePushWithFakeBlobReader(t *testing.T) {
	mem := &fakeBlobReader{files: map[string][]byte{
		"blobs/secret": []byte("sekrit\n"),
	}}
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/secret", Blob: "blobs/secret"},
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, mem); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(&buf, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanDir != dir {
		t.Fatalf("planDir=%q", got.PlanDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "blobs", "secret"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sekrit\n" {
		t.Fatalf("blob data %q", data)
	}
}

func TestDecodePushBareJSONL(t *testing.T) {
	raw, err := EncodePlan([]Op{{Op: KindPlan, Version: CurrentVersion, ID: "bare"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ops[0].ID != "bare" {
		t.Fatalf("%#v", got.Ops)
	}
}

func TestDecodePushRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	err := extractTarHeader(dir, &tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Size: 0}, bytes.NewReader(nil))
	if err == nil || !strings.Contains(err.Error(), "zip-slip") {
		t.Fatalf("want zip-slip error, got %v", err)
	}
}

func TestDecodePushBadMagic(t *testing.T) {
	_, err := DecodePush(strings.NewReader("NOPE\nblobs 0\nplan\n"), "")
	if err == nil || !strings.Contains(err.Error(), "bad magic") {
		t.Fatalf("want bad magic, got %v", err)
	}
}

// TestDecodePushRejectsSealedPlanAgeSkewWording pins the exact "old-gonf
// given a sealed plan.age on stdin" wording docs/plan-encryption.md
// documents ("Schema, versioning and remote skew"): a gonf binary that
// predates the sealed-apply sniff (internal/cli, task 3b2) would still call
// DecodePush directly on whatever stdin holds, and age's own cleartext
// version banner is not the GONF-PUSH/1 magic DecodePush expects — so it
// fails here, before any op is even parsed, with exactly the quoted-magic
// wording the doc promises. This is DecodePush's existing, unchanged
// behavior; the sniff added by task 3b2 only decides whether DecodePush is
// called at all for a given input, never what DecodePush itself does with
// bytes it is handed directly.
func TestDecodePushRejectsSealedPlanAgeSkewWording(t *testing.T) {
	_, err := DecodePush(strings.NewReader("age-encryption.org/v1\n-> X25519 ...\n"), "")
	want := `plan push: bad magic "age-encryption.org/v1"`
	if err == nil || err.Error() != want {
		t.Fatalf("DecodePush(sealed-looking stdin) = %v, want exactly %q", err, want)
	}
}

// TestPushHasBlobs pins PushHasBlobs' peek against the exact frames
// EncodePush/DecodePush themselves use, so a sealed apply's blob-vs-no-blob
// probe (internal/cli, task 3b2) never drifts from what DecodePush would
// actually do with the same bytes.
func TestPushHasBlobs(t *testing.T) {
	noBlobsFrame := func(t *testing.T) []byte {
		t.Helper()
		var buf bytes.Buffer
		if err := EncodePush(&buf, []Op{{Op: KindPlan, Version: CurrentVersion, ID: "p"}}, nil); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	withBlobsFrame := func(t *testing.T) []byte {
		t.Helper()
		mem := NewMemoryStore()
		ref, err := mem.WriteFile("secret", []byte("x\n"))
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		ops := []Op{
			{Op: KindPlan, Version: CurrentVersion, ID: "p"},
			{Op: KindFile, Path: "/tmp/x", Blob: ref},
		}
		if err := EncodePush(&buf, ops, mem); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	bareJSONL := func(t *testing.T) []byte {
		t.Helper()
		raw, err := EncodePlan([]Op{{Op: KindPlan, Version: CurrentVersion, ID: "bare"}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	tests := []struct {
		name string
		data func(t *testing.T) []byte
		want bool
	}{
		{"no blobs", noBlobsFrame, false},
		{"with blobs", withBlobsFrame, true},
		{"bare JSONL", bareJSONL, false},
		{"empty", func(t *testing.T) []byte { return nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.data(t)
			if got := PushHasBlobs(data); got != tt.want {
				t.Fatalf("PushHasBlobs(%q) = %v, want %v", data, got, tt.want)
			}
			// PushHasBlobs must agree with what DecodePush itself does: a
			// "false" frame decodes fine with planDir "", and a "true" frame
			// requires one (DecodePush's own contract) — this is the
			// invariant decryptAndDecodeSealedPush (internal/cli) relies on.
			_, err := DecodePush(bytes.NewReader(data), "")
			if tt.want && err == nil {
				t.Fatalf("DecodePush(%q, \"\") unexpectedly succeeded for a blobs frame", tt.name)
			}
			if !tt.want && tt.name != "empty" && err != nil {
				t.Fatalf("DecodePush(%q, \"\") = %v, want success for a no-blobs frame", tt.name, err)
			}
		})
	}
}

// gzipBomb gzip-compresses n zero bytes (a highly compressible payload: a
// few MB of zeros collapses to a few KB) and returns the compressed bytes,
// for a test that needs a small, fast-to-build stream with a large
// decompression ratio without constructing anything close to the
// multi-gigabyte repro task be2's own investigation measured against the
// real gonf binary.
func gzipBomb(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestMaybeGunzipCapsDecompressedOutput is task be2's regression test for
// the memory-amplification DoS: without a decompressed-output cap,
// maybeGunzip's io.ReadAll(gr) would read a gzip stream to completion no
// matter how large it inflates to. This uses maybeGunzip's max parameter
// (not the real MaxDecompressedPushPlan constant, task be2's own annotation
// notes this deliberately: the mechanism is proven at a small, fast scale,
// not by actually decompressing hundreds of megabytes in a test) to prove
// the cap fires, loudly, before the bomb's true 5 MiB is ever fully
// realized.
func TestMaybeGunzipCapsDecompressedOutput(t *testing.T) {
	const bombSize = 5 << 20 // 5 MiB of zeros
	bomb := gzipBomb(t, bombSize)
	if len(bomb) > 64<<10 {
		t.Fatalf("bomb compressed to %d bytes, expected a high compression ratio", len(bomb))
	}

	const testCap = 64 << 10 // artificially low test-only cap, well under bombSize
	out, err := maybeGunzip(bomb, testCap)
	if out != nil {
		t.Fatalf("maybeGunzip over cap returned %d bytes, want nil", len(out))
	}
	if !errors.Is(err, ErrPushPlanTooLarge) {
		t.Fatalf("maybeGunzip over cap error = %v, want ErrPushPlanTooLarge", err)
	}
}

// TestMaybeGunzipCapsDecompressedOutputBoundsMemory is
// TestMaybeGunzipCapsDecompressedOutput's "prove it, don't just assert the
// error" companion: it samples runtime.MemStats.TotalAlloc (a monotonic
// cumulative counter, unaffected by when GC happens to run, unlike
// HeapAlloc) around the same call and asserts the call did not allocate
// anywhere near the bomb's true 5 MiB decompressed size — mathematically
// guaranteed by construction (maybeGunzip wraps the gzip.Reader in
// io.LimitReader(gr, max+1) before io.ReadAll, so the decompressor is never
// asked to produce more than max+1 bytes regardless of what the compressed
// stream could otherwise expand to), and this test is the empirical check
// that the wiring actually behaves that way.
func TestMaybeGunzipCapsDecompressedOutputBoundsMemory(t *testing.T) {
	const bombSize = 5 << 20 // 5 MiB of zeros
	bomb := gzipBomb(t, bombSize)
	const testCap = 64 << 10 // artificially low test-only cap

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, err := maybeGunzip(bomb, testCap)
	runtime.ReadMemStats(&after)

	if out != nil || !errors.Is(err, ErrPushPlanTooLarge) {
		t.Fatalf("maybeGunzip(bomb, %d) = (%v bytes, %v), want (nil, ErrPushPlanTooLarge)", testCap, len(out), err)
	}
	const allocBound = 2 << 20 // 2 MiB: far below bombSize, comfortably above cap+overhead
	if delta := after.TotalAlloc - before.TotalAlloc; delta > allocBound {
		t.Fatalf("maybeGunzip(bomb, %d) allocated %d bytes, want under %d (bomb decompresses to %d if unbounded)",
			testCap, delta, allocBound, bombSize)
	}
}

// TestMaybeGunzipWithinCapUnaffected pins that a legitimate payload well
// under the cap decodes exactly as before this task's fix: the cap must
// never truncate or otherwise alter a stream that never approaches it.
func TestMaybeGunzipWithinCapUnaffected(t *testing.T) {
	ops := []Op{{Op: KindPlan, Version: CurrentVersion, ID: "small"}}
	raw, err := EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := maybeGunzip(buf.Bytes(), MaxDecompressedPushPlan)
	if err != nil {
		t.Fatalf("maybeGunzip within cap: %v", err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("maybeGunzip within cap = %q, want %q", out, raw)
	}
}
