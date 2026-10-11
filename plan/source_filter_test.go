package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type legacyBlobStore struct{}

func (legacyBlobStore) WriteFile(string, []byte) (string, error) { return "file", nil }
func (legacyBlobStore) WriteTree(string, string) (string, error) { return "tree", nil }
func (legacyBlobStore) WriteGlob(string, string) (string, error) { return "glob", nil }

func TestWriteSyncSourceRequiresFiltering(t *testing.T) {
	if _, err := WriteSyncSource(nil, "x", "src", "", nil); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := WriteSyncSource(legacyBlobStore{}, "x", "src", "", []string{"secret"}); err == nil {
		t.Fatal("legacy store silently included exclusions")
	}
	for _, glob := range []string{"", "*"} {
		got, err := WriteSyncSource(legacyBlobStore{}, "x", "src", glob, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := "tree"
		if glob != "" {
			want = "glob"
		}
		if got != want {
			t.Fatalf("got %s want %s", got, want)
		}
	}
}

func TestFilteredStoresExcludeBeforeReading(t *testing.T) {
	src := t.TempDir()
	for _, name := range []string{"empty", "private", "sub"} {
		if err := os.Mkdir(filepath.Join(src, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"keep": "public", "private/secret": "private-bytes", "sub/drop.tmp": "excluded", "sub/keep": "nested", "secret.tmpl": "secret template"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("missing", filepath.Join(src, "private-link")); err != nil {
		t.Fatal(err)
	}
	patterns := []string{"private", "private-link", "*.tmp", "secret"}
	mem := NewMemoryStore()
	disk := NewStore(t.TempDir())
	for _, glob := range []bool{false, true} {
		t.Run("source", func(t *testing.T) {
			tree, pattern := src, ""
			if glob {
				pattern = filepath.Join(src, "*")
			}
			ref, err := WriteSyncSource(mem, "source", tree, pattern, patterns)
			if err != nil {
				t.Fatal(err)
			}
			entries, ok := mem.TreeBlob(ref)
			if !ok {
				t.Fatal("no memory manifest")
			}
			diskRef, err := WriteSyncSource(disk, "source", tree, pattern, patterns)
			if err != nil {
				t.Fatal(err)
			}
			root, err := Resolve(disk.Root, diskRef)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := scanTree(root)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, entries) {
				t.Fatalf("stores differ: %v / %v", actual, entries)
			}
			var rels []string
			for _, e := range entries {
				rels = append(rels, e.Rel)
			}
			want := []string{"empty", "keep", "sub", "sub/keep"}
			if glob {
				want = []string{"keep"}
			}
			if !reflect.DeepEqual(rels, want) {
				t.Fatalf("manifest=%v want %v", rels, want)
			}
		})
	}
	for _, store := range []BlobStore{mem, disk} {
		if _, err := WriteSyncSource(store, "bad", src, "", []string{"["}); err == nil {
			t.Fatal("invalid pattern accepted")
		}
	}
}

func TestSyncExclusionWireAndVersion(t *testing.T) {
	op := Op{Op: KindSyncDir, Path: "/dst", Blob: "blobs/src", Payload: SyncDirPayload{Exclude: []string{"private", "*.tmp"}}}
	if RequiredVersion([]Op{op}) != VersionSyncDirExclude {
		t.Fatal("missing compatibility floor")
	}
	data, err := EncodeOp(op)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOp(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op, decoded) {
		t.Fatalf("round trip changed: %s", data)
	}
	if _, err := DecodeOp([]byte(`{"op":"file","path":"/dst","exclude":["private"]}`)); err == nil {
		t.Fatal("foreign exclusion field accepted")
	}
	if RequiredVersion([]Op{{Op: KindDir, Payload: SyncDirPayload{Exclude: []string{"private"}}}}) != VersionConfigSet {
		t.Fatal("foreign payload raised floor")
	}
	if !SupportsVersion(27) || !SupportsVersion(28) {
		t.Fatal("old/new schemas unsupported")
	}
}
