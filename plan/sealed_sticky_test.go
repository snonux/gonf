package plan

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sealedChunks is a two-chunk plan: an unprivileged chunk reading
// blobs/pub.txt, and an elevated chunk whose sensitive op reads
// blobs/sec.txt and whose non-sensitive op reads blobs/pub2.txt.
func sealedChunks() []Chunk {
	return []Chunk{
		{Ops: []Op{{Op: KindPlan}, {Op: KindFile, Blob: "blobs/pub.txt"}}},
		{Elevate: true, Ops: []Op{
			{Op: KindPlan},
			{Op: KindFile, Blob: "blobs/sec.txt", Sensitive: true},
			{Op: KindFile, Blob: "blobs/pub2.txt"},
			{Op: KindSyncDir, Blob: "blobs/tree", Sensitive: true},
		}},
	}
}

// SealedStickyRefs selects exactly the refs SensitiveElevatedBlobs' ops
// read, and ChunkNeedsStickyKey only the elevated chunk holding them.
func TestSealedStickyRefsSelection(t *testing.T) {
	chunks := sealedChunks()
	refs, err := SealedStickyRefs(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"blobs/sec.txt", "blobs/tree"}; !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	if got := SensitiveElevatedBlobs(chunks); len(got) != len(refs) {
		t.Fatalf("SensitiveElevatedBlobs = %v, want one entry per sealed op", got)
	}
	if ChunkNeedsStickyKey(chunks[0]) || !ChunkNeedsStickyKey(chunks[1]) {
		t.Fatal("ChunkNeedsStickyKey must hold only for the elevated chunk with the sensitive blob op")
	}
	// A sensitive blob op in an unprivileged chunk is not sealed: the login
	// user owns that content anyway.
	chunks[0].Ops[1].Sensitive = true
	if ChunkNeedsStickyKey(chunks[0]) {
		t.Fatal("unprivileged chunk must never need the key")
	}
}

// A ref read both by a sealed op and by any other op is refused, by
// position, never naming the ref.
func TestSealedStickyRefsRefusesSharedRef(t *testing.T) {
	chunks := sealedChunks()
	chunks[0].Ops[1].Blob = "blobs/sec.txt"
	_, err := SealedStickyRefs(chunks)
	if err == nil || !strings.Contains(err.Error(), "file op 1 of chunk 1") || strings.Contains(err.Error(), "sec.txt") {
		t.Fatalf("SealedStickyRefs(shared) = %v, want a positional refusal", err)
	}
}

// WriteRefArchive's archive unpacks, through the push extractor, to exactly
// that ref's members, for a file and a tree ref; a missing ref is refused.
func TestWriteRefArchiveRoundTrip(t *testing.T) {
	mem := NewMemoryStore()
	if _, err := mem.WriteFile("sec.txt", []byte("secret-a")); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("secret-b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.WriteTree("tree", src); err != nil {
		t.Fatal(err)
	}
	for ref, rel := range map[string]string{"blobs/sec.txt": "blobs/sec.txt", "blobs/tree": "blobs/tree/f"} {
		var buf bytes.Buffer
		if err := WriteRefArchive(&buf, mem, ref); err != nil {
			t.Fatalf("WriteRefArchive(%s): %v", ref, err)
		}
		dir := t.TempDir()
		if err := readBlobsGzipTar(bufio.NewReader(&buf), dir); err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(filepath.Join(dir, "blobs"))
		if len(entries) != 1 {
			t.Fatalf("%s: archive holds %d top-level refs, want only itself", ref, len(entries))
		}
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	if err := WriteRefArchive(&bytes.Buffer{}, mem, "blobs/missing"); err == nil {
		t.Fatal("WriteRefArchive(missing ref) = nil")
	}
}

// WithSealedRefs serves the sealed stream at its sealed path and never the
// sealed ref's plaintext; the upload tar therefore holds no plaintext of
// it, while an unsealed ref uploads unchanged.
func TestWithSealedRefsNeverServesPlaintext(t *testing.T) {
	mem := NewMemoryStore()
	for name, data := range map[string]string{"sec.txt": "PLAINTEXT-SECRET", "pub.txt": "public"} {
		if _, err := mem.WriteFile(name, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	up := WithSealedRefs(mem, map[string][]byte{"blobs/sec.txt": []byte("CIPHERTEXT")})
	if want := []string{"blobs/pub.txt", "sealed/blobs/sec.txt.age"}; !reflect.DeepEqual(up.Refs(), want) {
		t.Fatalf("Refs = %v, want %v", up.Refs(), want)
	}
	if _, ok := up.FileBlob("blobs/sec.txt"); ok {
		t.Fatal("sealed ref's plaintext is served")
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, []Op{{Op: KindPlan, Version: CurrentVersion}}, up); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := DecodePush(bytes.NewReader(buf.Bytes()), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs", "sec.txt")); !os.IsNotExist(err) {
		t.Fatalf("plaintext sealed ref uploaded: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "sealed", "blobs", "sec.txt.age")); string(got) != "CIPHERTEXT" {
		t.Fatalf("sealed member = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "blobs", "pub.txt")); string(got) != "public" {
		t.Fatalf("unsealed ref = %q", got)
	}
	if empty := WithSealedRefs(mem, nil); !reflect.DeepEqual(empty.Refs(), mem.Refs()) {
		t.Fatalf("WithSealedRefs(nil).Refs = %v, want mem's", empty.Refs())
	}
}
