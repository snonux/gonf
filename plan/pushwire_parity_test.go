package plan

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memTree packages a source tree into a memory store and returns the
// store plus the blob ref.
func memTree(t *testing.T, src string) (*MemoryStore, string) {
	t.Helper()
	m := NewMemoryStore()
	ref, err := m.WriteTree("app", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.TreeBlob(ref); !ok {
		t.Fatal("missing tree")
	}
	return m, ref
}

func TestWriteTreeTarCarriesSymlinksAndEmptyDirs(t *testing.T) {
	src := buildManifestSourceTree(t) // full manifest, dangling included
	mem, ref := memTree(t, src)

	buf := &bytes.Buffer{}
	if err := writeBlobsGzipTar(buf, mem); err != nil {
		t.Fatal(err)
	}
	assertTreeTarHeaders(t, buf.Bytes(), ref)
}

// assertTreeTarHeaders re-decodes the gzip+tar blob phase and pins the
// header shape: the tree root and explicit dirs as TypeDir (each exactly
// once), symlinks as TypeSymlink with the raw Linkname, files as TypeReg.
func assertTreeTarHeaders(t *testing.T, raw []byte, ref string) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var names []string
	seen := make(map[string]int)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		seen[hdr.Name]++
		switch hdr.Typeflag {
		case tar.TypeDir:
			if hdr.Mode&0o077 != 0 {
				t.Fatalf("dir header %s mode %#o leaks group/other bits", hdr.Name, hdr.Mode)
			}
		case tar.TypeSymlink:
			switch strings.TrimPrefix(hdr.Name, ref+"/") {
			case "rel-link":
				if hdr.Linkname != "a.conf" {
					t.Fatalf("%s Linkname %q", hdr.Name, hdr.Linkname)
				}
			case "dangling":
				if hdr.Linkname != "no-such-target" {
					t.Fatalf("%s Linkname %q", hdr.Name, hdr.Linkname)
				}
			case "sub/up-link":
				if hdr.Linkname != "../a.conf" {
					t.Fatalf("%s Linkname %q", hdr.Name, hdr.Linkname)
				}
			default:
				t.Fatalf("unexpected symlink header %s", hdr.Name)
			}
		case tar.TypeReg:
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			switch strings.TrimPrefix(hdr.Name, ref+"/") {
			case "a.conf":
				if string(data) != "a\n" {
					t.Fatalf("%s content %q", hdr.Name, data)
				}
			case "sub/b.conf":
				if string(data) != "b\n" {
					t.Fatalf("%s content %q", hdr.Name, data)
				}
			default:
				t.Fatalf("unexpected file header %s", hdr.Name)
			}
		default:
			t.Fatalf("unexpected tar typeflag %#x for %s", hdr.Typeflag, hdr.Name)
		}
	}
	want := []string{
		ref + "/",
		ref + "/a.conf",
		ref + "/dangling",
		ref + "/empty.d/",
		ref + "/rel-link",
		ref + "/sub/",
		ref + "/sub/b.conf",
		ref + "/sub/up-link",
	}
	if len(names) != len(want) {
		t.Fatalf("tar headers = %#v, want %#v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("tar header[%d] = %q, want %q", i, n, want[i])
		}
		if seen[n] != 1 {
			t.Fatalf("tar header %q emitted %d times", n, seen[n])
		}
	}
}

func TestExtractTarHeaderRecreatesSymlinks(t *testing.T) {
	planDir := t.TempDir()
	raw := tarBytes(t, []tarEntry{
		{name: "blobs/app/", typeflag: tar.TypeDir},
		{name: "blobs/app/rel-link", typeflag: tar.TypeSymlink, linkname: "a.conf"},
		{name: "blobs/app/dangling", typeflag: tar.TypeSymlink, linkname: "no-such-target"},
	})
	if err := readBlobsGzipTar(bufio.NewReader(bytes.NewReader(raw)), planDir); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"blobs/app/rel-link": "a.conf",
		"blobs/app/dangling": "no-such-target",
	} {
		path := filepath.Join(planDir, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s: want symlink, got mode %#o", path, info.Mode())
		}
		if got, err := os.Readlink(path); err != nil || got != want {
			t.Fatalf("%s: Readlink %q (%v), want %q", path, got, err, want)
		}
	}
}

func TestExtractTarHeaderReplacesPreExistingForSymlink(t *testing.T) {
	planDir := t.TempDir()
	existing := filepath.Join(planDir, "blobs", "app", "rel-link")
	if err := os.MkdirAll(filepath.Dir(existing), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hdr := &tar.Header{Name: "blobs/app/rel-link", Typeflag: tar.TypeSymlink, Linkname: "a.conf"}
	var written int64
	if err := extractTarHeader(planDir, hdr, bytes.NewReader(nil), &written, MaxExtractedPushBlobs); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(existing); err != nil || got != "a.conf" {
		t.Fatalf("Readlink %q (%v), want a.conf", got, err)
	}
}

// tarEntry is a minimal tar header description for tarBytes.
type tarEntry struct {
	name     string
	typeflag byte
	linkname string
	data     []byte
}

// tarBytes gzips+tar-codes the entries into the blobs-phase wire format.
func tarBytes(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	gz := gzip.NewWriter(buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Linkname: e.linkname}
		if e.typeflag == tar.TypeReg {
			hdr.Mode = 0o600
			hdr.Size = int64(len(e.data))
		} else {
			hdr.Mode = 0o700
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(e.data) > 0 {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestSyncDirParityDiskStoreVsPush is the marquee parity assertion: ONE
// source tree (file + empty dir + symlinks) applied locally via the disk
// blob store must produce EXACTLY the same destination as the same tree
// pushed through the memory store + GONF-PUSH tar + remote extraction:
// same entries, same file contents, same symlink presence and raw
// targets, same empty directory. The dangling symlink is removed from the
// source first: link.Ensure refuses dangling links on apply (documented
// policy, identical on both transports — see the dedicated test below).
func TestSyncDirParityDiskStoreVsPush(t *testing.T) {
	src := buildManifestSourceTree(t)
	if err := os.Remove(filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}

	// (a) Local transport: disk Store packages into a planDir blob tree.
	planDirA := t.TempDir()
	store := NewStore(planDirA)
	refA, err := store.WriteTree("app", src)
	if err != nil {
		t.Fatal(err)
	}
	dstA := filepath.Join(t.TempDir(), "app")
	if err := Apply(parityOps(refA, dstA), Facts{}, planDirA); err != nil {
		t.Fatalf("apply disk-store plan: %v", err)
	}

	// (b) Push transport: memory store + gzip tar + extraction.
	planDirB := t.TempDir()
	mem, refB := memTree(t, src)
	buf := &bytes.Buffer{}
	if err := writeBlobsGzipTar(buf, mem); err != nil {
		t.Fatal(err)
	}
	if err := readBlobsGzipTar(bufio.NewReader(bytes.NewReader(buf.Bytes())), planDirB); err != nil {
		t.Fatal(err)
	}
	dstB := filepath.Join(t.TempDir(), "app")
	if err := Apply(parityOps(refB, dstB), Facts{}, planDirB); err != nil {
		t.Fatalf("apply pushed plan: %v", err)
	}

	// The two destinations must be identical trees.
	assertDiskTree(t, dstA, applicableManifest())
	assertDiskTree(t, dstB, applicableManifest())
}

// TestSyncDirDanglingSymlinkFailsIdentically pins the refusal parity for
// dangling links: link.Ensure treats them as apply failures, so BOTH
// transports must fail with the same refusal — packaging still preserves
// the raw target (the transport never drops or rewrites source content).
func TestSyncDirDanglingSymlinkFailsIdentically(t *testing.T) {
	src := buildManifestSourceTree(t) // includes the dangling symlink

	planDirA := t.TempDir()
	store := NewStore(planDirA)
	refA, err := store.WriteTree("app", src)
	if err != nil {
		t.Fatal(err)
	}
	errA := Apply(parityOps(refA, filepath.Join(t.TempDir(), "app")), Facts{}, planDirA)
	if errA == nil || !strings.Contains(errA.Error(), "refusing broken link") {
		t.Fatalf("disk-store transport: want dangling-link refusal, got %v", errA)
	}

	planDirB := t.TempDir()
	mem, refB := memTree(t, src)
	buf := &bytes.Buffer{}
	if err := writeBlobsGzipTar(buf, mem); err != nil {
		t.Fatal(err)
	}
	if err := readBlobsGzipTar(bufio.NewReader(bytes.NewReader(buf.Bytes())), planDirB); err != nil {
		t.Fatal(err)
	}
	errB := Apply(parityOps(refB, filepath.Join(t.TempDir(), "app")), Facts{}, planDirB)
	if errB == nil || !strings.Contains(errB.Error(), "refusing broken link") {
		t.Fatalf("push transport: want dangling-link refusal, got %v", errB)
	}
	if !strings.HasSuffix(errA.Error(), danglingRefusalSuffix) ||
		!strings.HasSuffix(errB.Error(), danglingRefusalSuffix) {
		t.Fatalf("refusals diverge:\n local: %v\n push:  %v", errA, errB)
	}
}

// danglingRefusalSuffix is the transport-independent tail of the
// link.Ensure refusal for the canonical dangling symlink.
const danglingRefusalSuffix = `dangling: refusing broken link to "no-such-target" (target does not exist)`

// parityOps builds the sync_dir plan for one transport's blob ref.
func parityOps(ref, dst string) []Op {
	return []Op{
		header(),
		{Op: KindSyncDir, Path: dst, Mode: "0700", Blob: ref, Prune: true},
	}
}

// TestGlobParityDiskStoreVsPush pins glob-blob parity: a glob source with a
// symlink-to-file must yield the same REGULAR file (read-through content) at
// the destination whether packaged via the disk store or the push wire —
// the flat glob policy (dangling links are skipped by packaging).
func TestGlobParityDiskStoreVsPush(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real.conf"), []byte("cfg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.conf", filepath.Join(src, "tofile.conf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(src, "dangling.conf")); err != nil {
		t.Fatal(err)
	}

	// Transport A: disk store.
	planDirA := t.TempDir()
	store := NewStore(planDirA)
	refA, err := store.WriteGlob("units", filepath.Join(src, "*.conf"))
	if err != nil {
		t.Fatal(err)
	}

	// Transport B: memory store + gzip tar + extraction.
	planDirB := t.TempDir()
	mem, refB := memGlob(t, src)
	_ = mem
	var buf bytes.Buffer
	if err := writeBlobsGzipTar(&buf, mem); err != nil {
		t.Fatal(err)
	}
	if err := readBlobsGzipTar(bufio.NewReader(bytes.NewReader(buf.Bytes())), planDirB); err != nil {
		t.Fatal(err)
	}

	// Both destinations must contain exactly the read-through regular files
	// (the dangling link is skipped by packaging; nothing dangles).
	want := []BlobEntry{
		{Rel: "real.conf", Kind: BlobFile, Data: []byte("cfg\n")},
		{Rel: "tofile.conf", Kind: BlobFile, Data: []byte("cfg\n")},
	}
	assertDiskTree(t, filepath.Join(planDirA, refA), want)
	assertDiskTree(t, filepath.Join(planDirB, refB), want)
}

// memGlob packages glob matches into a memory store and returns the store
// plus the blob ref.
func memGlob(t *testing.T, src string) (*MemoryStore, string) {
	t.Helper()
	m := NewMemoryStore()
	ref, err := m.WriteGlob("units", filepath.Join(src, "*.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return m, ref
}
