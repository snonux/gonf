package plan

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestEncodeDecodePushNoBlobs(t *testing.T) {
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/x", Payload: FilePayload{ContentB64: "eA=="}},
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
	var written int64
	err := extractTarHeader(dir, &tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Size: 0}, bytes.NewReader(nil), &written, MaxExtractedPushBlobs)
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
// given a sealed plan.age on stdin" wording docs/design/plan-encryption.md
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
// maybeGunzip would read a gzip stream to completion no matter how large it
// inflates to. This uses maybeGunzip's max parameter (not the real
// MaxDecompressedPushPlan var, task be2's own annotation notes this
// deliberately: the mechanism is proven at a small, fast scale, not by
// actually decompressing hundreds of megabytes in a test) to prove the cap
// fires, loudly, before the bomb's true 5 MiB is ever fully realized.
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
// anywhere near the bomb's true 5 MiB decompressed size. readCapped (task
// 3g2) reads in pushPlanReadChunk-sized chunks and refuses the moment the
// running total exceeds the cap, so the decompressor is never asked to
// produce more than about one chunk past testCap regardless of what the
// compressed stream could otherwise expand to — this test is the empirical
// check that the wiring actually behaves that way.
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
// buildBlobsBombFrame builds a standalone gzip+tar stream — exactly the
// shape writeBlobsGzipTar produces and readBlobsGzipTarCapped consumes,
// without going through the full GONF-PUSH/1 frame — containing one regular
// file entry named name whose declared tar header Size is size but whose
// actual data is a highly compressible run of zero bytes: a few MB of zeros
// collapses to a few KB once gzipped (the same trick gzipBomb above uses for
// the plan-section cap's own regression test), so this stays small and fast
// to build without approaching the multi-gigabyte scale task 2g2's own probe
// measured against the real binary.
func buildBlobsBombFrame(t *testing.T, name string, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(size)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(make([]byte, size)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dirEntryNames lists dir's entries recursively (relative, slash-joined),
// for asserting exactly what a refused or successful extraction left behind
// on disk.
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// TestReadBlobsGzipTarCapsExtractedBytes is task 2g2's regression test for
// the disk-exhaustion DoS: readBlobsGzipTar used to call extractTarFile with
// an unbounded io.Copy and never checked hdr.Size before extracting, so a
// single tar entry whose declared size alone exceeds the budget must now be
// refused loudly and IMMEDIATELY — before any of its bytes are written — and
// must leave the destination directory completely empty, not a
// partially-extracted bomb.
func TestReadBlobsGzipTarCapsExtractedBytes(t *testing.T) {
	const bombSize = 8 << 20 // 8 MiB of zeros
	frame := buildBlobsBombFrame(t, "blobs/bomb", bombSize)
	if len(frame) > 64<<10 {
		t.Fatalf("bomb compressed to %d bytes, expected a high compression ratio", len(frame))
	}

	const testCap = 64 << 10 // artificially low test-only cap, well under bombSize
	dir := t.TempDir()
	err := readBlobsGzipTarCapped(bufio.NewReader(bytes.NewReader(frame)), dir, testCap)
	if !errors.Is(err, ErrPushBlobsTooLarge) {
		t.Fatalf("readBlobsGzipTarCapped over cap error = %v, want ErrPushBlobsTooLarge", err)
	}
	if got := dirEntryNames(t, dir); len(got) != 0 {
		t.Fatalf("refused extraction left behind %v, want an empty directory", got)
	}
}

// TestReadBlobsGzipTarCapsCumulativeAcrossEntries proves the budget is a
// single RUNNING counter shared across every entry in the archive, not a
// per-file check alone: two entries that each individually fit under the cap
// still trip it once their sizes add up past it, and the entry that trips it
// leaves nothing partial behind (only the entries that genuinely fit within
// budget before it are present).
func TestReadBlobsGzipTarCapsCumulativeAcrossEntries(t *testing.T) {
	const perEntry = 40 << 10 // 40 KiB each
	const testCap = 64 << 10  // two entries (80 KiB) together exceed this

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	names := []string{"blobs/a", "blobs/b"}
	for _, name := range names {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: perEntry}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(bytes.Repeat([]byte{'x'}, perEntry)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	err := readBlobsGzipTarCapped(bufio.NewReader(bytes.NewReader(buf.Bytes())), dir, testCap)
	if !errors.Is(err, ErrPushBlobsTooLarge) {
		t.Fatalf("readBlobsGzipTarCapped over cumulative cap error = %v, want ErrPushBlobsTooLarge", err)
	}
	got := dirEntryNames(t, dir)
	sort.Strings(got)
	want := []string{"blobs", "blobs/a"} // the parent dir extractTarFile's MkdirAll creates, plus the one entry that fit
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cumulative refusal left %v, want exactly %v (only the entry that fit before the cap tripped, no trace of blobs/b)", got, want)
	}
}

// TestReadBlobsGzipTarWithinCapExtractsFully pins that a legitimate blob set
// well within the cap is entirely unaffected by this task's fix: every file
// extracts completely, with the exact content and byte count it had before.
func TestReadBlobsGzipTarWithinCapExtractsFully(t *testing.T) {
	mem := NewMemoryStore()
	refA, err := mem.WriteFile("a", bytes.Repeat([]byte("legit-a"), 1000))
	if err != nil {
		t.Fatal(err)
	}
	refB, err := mem.WriteFile("b", bytes.Repeat([]byte("legit-b"), 2000))
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writeBlobsGzipTar(&buf, mem); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// MaxExtractedPushBlobs (1 GiB) comfortably covers this small legitimate
	// set; use the real exported constant here (unlike the two capped tests
	// above) to also prove the public readBlobsGzipTar/DecodePush path — not
	// just the max-parameterized test seam — extracts a normal push cleanly.
	if err := readBlobsGzipTarCapped(bufio.NewReader(&buf), dir, MaxExtractedPushBlobs); err != nil {
		t.Fatalf("readBlobsGzipTarCapped within cap: %v", err)
	}

	gotA, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(refA)))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != strings.Repeat("legit-a", 1000) {
		t.Fatalf("blob a content mismatch, got %d bytes", len(gotA))
	}
	gotB, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(refB)))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotB) != strings.Repeat("legit-b", 2000) {
		t.Fatalf("blob b content mismatch, got %d bytes", len(gotB))
	}
}

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

// TestDecodePushWithinLoweredCapEndToEnd is task 3g2's confirmation that
// lowering MaxDecompressedPushPlan from 256 MiB to 64 MiB did not turn it
// into a wall a real, legitimate push ever hits: it builds a push frame
// whose decompressed plan section is a few MiB of genuine ops JSONL (well
// beyond this repo's own real recorded plans, which run to tens of KB, but
// still comfortably under the new cap) and round-trips it through the full
// EncodePush/DecodePush path — the same path a real `gonf apply` or `gonf
// push` uses — rather than calling maybeGunzip directly.
func TestDecodePushWithinLoweredCapEndToEnd(t *testing.T) {
	const wantFileOps = 20000 // JSONL for this many ops decompresses to a few MiB
	ops := make([]Op, 0, wantFileOps+1)
	ops = append(ops, Op{Op: KindPlan, Version: CurrentVersion, ID: "3g2"}) // required header op
	for i := 0; i < wantFileOps; i++ {
		ops = append(ops, Op{
			Op:      KindFile,
			Path:    fmt.Sprintf("/tmp/3g2/%d", i),
			Payload: FilePayload{ContentB64: "aGVsbG8gd29ybGQK"}, // "hello world\n"
		})
	}
	wantOps := len(ops)

	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, nil); err != nil {
		t.Fatal(err)
	}
	if int64(buf.Len()) >= MaxDecompressedPushPlan {
		t.Fatalf("test setup: encoded frame %d bytes is not usefully smaller than the cap %d", buf.Len(), MaxDecompressedPushPlan)
	}

	got, err := DecodePush(&buf, "")
	if err != nil {
		t.Fatalf("DecodePush within lowered cap: %v", err)
	}
	if len(got.Ops) != wantOps {
		t.Fatalf("DecodePush within lowered cap: got %d ops, want %d", len(got.Ops), wantOps)
	}
	if got.Ops[0].ID != ops[0].ID || got.Ops[1].Path != ops[1].Path || got.Ops[wantOps-1].Path != ops[wantOps-1].Path {
		t.Fatalf("DecodePush within lowered cap: content mismatch, header=%q first=%q last=%q", got.Ops[0].ID, got.Ops[1].Path, got.Ops[wantOps-1].Path)
	}
}

// TestMaxDecompressedPushPlanOverride is task 3g2's confirmation that the
// override mechanism genuinely works: MaxDecompressedPushPlan is a var
// precisely so an embedder with an unusually large legitimate plan can
// raise it past the default before calling DecodePush, rather than hitting
// a hard, unfixable wall. This builds a decompressed payload larger than
// the DEFAULT cap but within a raised override, confirms it is refused at
// the default and then confirms the SAME payload succeeds once the var is
// raised, restoring the original value afterward so this test cannot leak
// state into any other test in the package.
func TestMaxDecompressedPushPlanOverride(t *testing.T) {
	original := MaxDecompressedPushPlan
	t.Cleanup(func() { MaxDecompressedPushPlan = original })

	ops := []Op{{Op: KindPlan, Version: CurrentVersion, ID: "p"}}
	raw, err := EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	// Pad the plan section's decompressed size past the default cap with a
	// trailing run of a byte DecodePlanBytes would reject as malformed JSON
	// were it ever reached — it never is here, since maybeGunzip's cap
	// check happens before any of the decompressed bytes are decoded as a
	// plan. Kept incompressible-ish (not literally, since gzip.Writer will
	// still compress padding bytes efficiently, but the padding's CONTENT
	// is irrelevant: only its decompressed LENGTH matters for this test) at
	// default cap + 1 MiB, so it must be refused under the default and
	// accepted only once the override raises the cap past it.
	padded := append(append([]byte{}, raw...), bytes.Repeat([]byte{'x'}, int(original)+(1<<20)-len(raw))...)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(padded); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	frame := buf.Bytes()

	if _, err := maybeGunzip(frame, MaxDecompressedPushPlan); !errors.Is(err, ErrPushPlanTooLarge) {
		t.Fatalf("maybeGunzip at default cap = %v, want ErrPushPlanTooLarge", err)
	}

	MaxDecompressedPushPlan = original + (2 << 20) // override: default cap + 2 MiB
	out, err := maybeGunzip(frame, MaxDecompressedPushPlan)
	if err != nil {
		t.Fatalf("maybeGunzip under raised override: %v", err)
	}
	if !bytes.Equal(out, padded) {
		t.Fatalf("maybeGunzip under raised override returned %d bytes, want %d", len(out), len(padded))
	}
}
