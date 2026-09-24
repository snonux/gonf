package plan

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/sealeddir"
	"github.com/snonux/gonf/resource"
)

// refArchive is WriteRefArchive(mem, ref) as bytes.
func refArchive(t *testing.T, mem BlobReader, ref string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteRefArchive(&buf, mem, ref); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sealedTarEntry is one member for gzipTarOf.
type sealedTarEntry struct {
	name, body, link string
	typ              byte
}

// gzipTarOf builds a gzip-compressed tar holding entries in order.
func gzipTarOf(t *testing.T, entries ...sealedTarEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		// Only regular members carry a body (archive/tar refuses data on
		// the other types); the others pass an empty one.
		hdr := &tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: e.typ, Linkname: e.link}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
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

// A file ref and a tree ref (with a subdirectory and a symlink) extract
// through one extractor into one directory, exactly as the push extractor
// would lay them out.
func TestSealedRefExtractorRoundTrip(t *testing.T) {
	mem := NewMemoryStore()
	if _, err := mem.WriteFile("sec.txt", []byte("secret-a")); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "f"), []byte("secret-b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/f", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.WriteTree("tree", src); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	x := NewSealedRefExtractor(dir)
	for _, ref := range []string{"blobs/sec.txt", "blobs/tree"} {
		if err := x.Extract(bytes.NewReader(refArchive(t, mem, ref)), ref); err != nil {
			t.Fatalf("Extract(%s): %v", ref, err)
		}
	}
	if got, err := ReadFile(dir, "blobs/sec.txt"); err != nil || string(got) != "secret-a" {
		t.Fatalf("file ref = %q, %v", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "blobs", "tree", "sub", "f")); string(got) != "secret-b" {
		t.Fatalf("tree ref file = %q", got)
	}
	if target, err := os.Readlink(filepath.Join(dir, "blobs", "tree", "link")); err != nil || target != "sub/f" {
		t.Fatalf("tree ref symlink = %q, %v", target, err)
	}
}

// Every archive that is not exactly its ref's own members is refused with
// ErrSealedRefArchive, and no refusal names the ref or a member.
func TestSealedRefExtractorRefusals(t *testing.T) {
	mem := NewMemoryStore()
	if _, err := mem.WriteFile("sec.txt", []byte("secret-a")); err != nil {
		t.Fatal(err)
	}
	good := refArchive(t, mem, "blobs/sec.txt")
	cases := map[string]struct {
		ref  string
		data []byte
	}{
		"invalid ref":        {"../etc/sec.txt", good},
		"not gzip":           {"blobs/sec.txt", []byte("plain bytes")},
		"truncated gzip":     {"blobs/sec.txt", good[:len(good)-6]},
		"trailing data":      {"blobs/sec.txt", append(append([]byte{}, good...), 'x')},
		"empty archive":      {"blobs/sec.txt", gzipTarOf(t)},
		"other ref":          {"blobs/other.txt", good},
		"extra member":       {"blobs/sec.txt", gzipTarOf(t, sealedTarEntry{name: "blobs/sec.txt", body: "a", typ: tar.TypeReg}, sealedTarEntry{name: "blobs/zzz.txt", body: "b", typ: tar.TypeReg})},
		"prefix sibling":     {"blobs/sec", gzipTarOf(t, sealedTarEntry{name: "blobs/sec.txt", body: "a", typ: tar.TypeReg})},
		"hardlink member":    {"blobs/tree", gzipTarOf(t, sealedTarEntry{name: "blobs/tree/h", link: "/etc/shadow", typ: tar.TypeLink})},
		"file ref as dir":    {"blobs/sec.txt", gzipTarOf(t, sealedTarEntry{name: "blobs/sec.txt", typ: tar.TypeDir})},
		"zip-slip in member": {"blobs/tree", gzipTarOf(t, sealedTarEntry{name: "blobs/tree/../../escape", body: "x", typ: tar.TypeReg})},
	}
	for name, c := range cases {
		dir := t.TempDir()
		err := NewSealedRefExtractor(dir).Extract(bytes.NewReader(c.data), c.ref)
		if _, statErr := os.Lstat(filepath.Join(dir, "escape")); !os.IsNotExist(statErr) {
			t.Fatalf("%s: a member landed outside its ref: %v", name, statErr)
		}
		if !errors.Is(err, ErrSealedRefArchive) {
			t.Fatalf("%s: err = %v, want ErrSealedRefArchive", name, err)
		}
		for _, leak := range []string{"sec", "zzz", "other", "etc", "shadow"} {
			if strings.Contains(strings.TrimPrefix(err.Error(), ErrSealedRefArchive.Error()), leak) {
				t.Fatalf("%s: error %q names %q", name, err, leak)
			}
		}
	}
}

// The extraction budget is shared across one extractor's refs: two refs
// that each fit alone are refused together once their sum exceeds it.
func TestSealedRefExtractorSharedBudget(t *testing.T) {
	mem := NewMemoryStore()
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := mem.WriteFile(name, bytes.Repeat([]byte("x"), 600)); err != nil {
			t.Fatal(err)
		}
	}
	x := NewSealedRefExtractor(t.TempDir())
	x.max = 1000
	if err := x.Extract(bytes.NewReader(refArchive(t, mem, "blobs/a.txt")), "blobs/a.txt"); err != nil {
		t.Fatal(err)
	}
	err := x.Extract(bytes.NewReader(refArchive(t, mem, "blobs/b.txt")), "blobs/b.txt")
	if !errors.Is(err, ErrPushBlobsTooLarge) {
		t.Fatalf("second ref over the shared budget: %v, want ErrPushBlobsTooLarge", err)
	}
}

// KeyedChunkSealedOps picks, by position, the sensitive blob-backed ops of
// a keyed chunk: the same selection the controller sealed.
func TestKeyedChunkSealedOps(t *testing.T) {
	ops := sealedChunks()[1].Ops
	if got, want := KeyedChunkSealedOps(ops), []int{1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("KeyedChunkSealedOps = %v, want %v", got, want)
	}
	if got := KeyedChunkSealedOps(sealedChunks()[0].Ops); got != nil {
		t.Fatalf("no sensitive blob op: %v", got)
	}
}

// PushIsKeyed recognises only the GONF-PUSH/2 magic line.
func TestPushIsKeyed(t *testing.T) {
	for data, want := range map[string]bool{
		"GONF-PUSH/2\nkey x\n": true,
		"GONF-PUSH/2":          false,
		"GONF-PUSH/1\n":        false,
		"GONF-PUSH/20\n":       false,
		"{\"op\":\"plan\"}\n":  false,
	} {
		if got := PushIsKeyed([]byte(data)); got != want {
			t.Fatalf("PushIsKeyed(%q) = %v, want %v", data, got, want)
		}
	}
}

// planDirRecorder is a Handler that records the PlanDir it is given.
type planDirRecorder struct{ got map[string]string }

func (r *planDirRecorder) ToOp(resource.PlanDraft) (Op, error) { return Op{}, nil }
func (r *planDirRecorder) Apply(op Op, ctx ApplyContext) error {
	r.got[op.Blob] = ctx.PlanDir
	return nil
}

// Under a sealeddir override, an op reading a sealed ref gets the private
// dir as its PlanDir and every other op keeps the apply's planDir.
func TestApplyResolvesSealedRefsToPrivateDir(t *testing.T) {
	const kind Kind = "test_sealed_plan_dir"
	rec := &planDirRecorder{got: map[string]string{}}
	handlers[kind] = rec
	t.Cleanup(func() { delete(handlers, kind) })
	ctx := sealeddir.With(context.Background(), "/private", []string{"blobs/sec.txt"})
	for _, ref := range []string{"blobs/sec.txt", "blobs/pub.txt", ""} {
		if err := applyActiveWithFacts(ctx, Op{Op: kind, Blob: ref}, "/sticky", Facts{}); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"blobs/sec.txt": "/private", "blobs/pub.txt": "/sticky", "": "/sticky"}
	if !reflect.DeepEqual(rec.got, want) {
		t.Fatalf("PlanDir per ref = %v, want %v", rec.got, want)
	}
}
