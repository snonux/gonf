package plan

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/testutil"
)

// swapForSymlink moves the directory path aside (to path+".orig") and puts a
// symlink to target in its place: what an attacker with write access to the
// parent of path would do between a check of path and its use. It returns the
// directory's new name.
func swapForSymlink(t *testing.T, path, target string) string {
	t.Helper()
	moved := path + ".orig"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	return moved
}

// onceBlobsDirVerified runs fn at the blobsDirVerified seam, the first time
// only, and restores the seam when the test ends.
func onceBlobsDirVerified(t *testing.T, fn func(blobsDir string)) {
	t.Helper()
	done := false
	prev := blobsDirVerified
	blobsDirVerified = func(blobsDir string) {
		if !done {
			done = true
			fn(blobsDir)
		}
	}
	t.Cleanup(func() { blobsDirVerified = prev })
}

// TestStoreWritesStayInVerifiedBlobsDir: blobs/ swapped for a symlink AFTER it
// was verified (and before anything was written below it) must not redirect
// the write, for every blob kind. The blob lands in the directory that was
// verified (now blobs.orig), and the symlink's target stays empty. Tree and
// glob blobs used to be cleared and filled by path after the check, so the
// swap redirected them into the target.
func TestStoreWritesStayInVerifiedBlobsDir(t *testing.T) {
	for _, mode := range storeModes() {
		for name, write := range blobWriters(t) {
			t.Run(mode.name+" "+name, func(t *testing.T) {
				root := testutil.PrivateTempDir(t) // 0700 whatever the umask: held mode verifies it
				elsewhere := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
				var moved string
				onceBlobsDirVerified(t, func(blobsDir string) {
					moved = swapForSymlink(t, blobsDir, elsewhere)
				})
				if err := write(mode.open(t, root)); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				requireEntries(t, elsewhere)
				if entries, err := os.ReadDir(moved); err != nil || len(entries) != 1 {
					t.Fatalf("verified blobs/ holds %v (%v), want the one blob", entries, err)
				}
			})
		}
	}
}

// storeMode opens a Store of one mode over root for a test.
type storeMode struct {
	name string
	open func(t *testing.T, root string) *Store
}

// storeModes is the path mode (NewStore) and the held mode (OpenSecureStore,
// closed when the test ends).
func storeModes() []storeMode {
	return []storeMode{
		{"path", func(_ *testing.T, root string) *Store { return NewStore(root) }},
		{"held", openHeldStore},
	}
}

// openHeldStore is OpenSecureStore(root) that fails the test on an error and
// closes the store when the test ends.
func openHeldStore(t *testing.T, root string) *Store {
	t.Helper()
	s, err := OpenSecureStore(root)
	if err != nil {
		t.Fatalf("OpenSecureStore(%s): %v", root, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestSecureStoreIgnoresPlanDirSwappedAfterOpen: the plan directory an
// operator named, swapped for a symlink after OpenSecureStore verified it,
// must not redirect any blob kind. A held-mode store writes relative to the
// verified directory's descriptor, so the blob lands there (now <dir>.orig)
// and the symlink's target stays empty. A path-mode store would follow the
// link, which is why the operator's directory is opened in held mode.
func TestSecureStoreIgnoresPlanDirSwappedAfterOpen(t *testing.T) {
	for name, write := range blobWriters(t) {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "out")
			store := openHeldStore(t, root)
			elsewhere := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
			moved := swapForSymlink(t, root, elsewhere)
			if err := write(store); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			requireEntries(t, elsewhere)
			requireEntries(t, moved, "blobs")
			assertOwnerOnlyDir(t, filepath.Join(moved, "blobs"))
		})
	}
}

// TestOpenSecureStoreRefusals: the held-mode store refuses what SecureDir
// refuses (a symlinked or dangling-symlink plan directory, a symlinked
// ancestor, a file, a world-writable or foreign-owned directory), and creates
// or changes nothing while doing so.
func TestOpenSecureStoreRefusals(t *testing.T) {
	root := t.TempDir()
	realDir := testutil.MkdirMode(t, filepath.Join(root, "real"), 0o700)
	for name, target := range map[string]string{"link": realDir, "dangling": filepath.Join(root, "nowhere")} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	open := testutil.MkdirMode(t, filepath.Join(root, "open"), 0o700)
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ dir, want string }{
		"symlink":            {filepath.Join(root, "link"), "is a symlink"},
		"dangling symlink":   {filepath.Join(root, "dangling"), "is a symlink"},
		"symlinked ancestor": {filepath.Join(root, "link", "new"), "is a symlink"},
		"file":               {filepath.Join(root, "file"), "not a directory"},
		"world-writable":     {open, "world-writable"},
	} {
		t.Run(name, func(t *testing.T) {
			before := testutil.Snapshot(t, root)
			s, err := OpenSecureStore(tc.dir)
			if err == nil {
				_ = s.Close()
				t.Fatalf("OpenSecureStore(%s) = nil, want a %q refusal", tc.dir, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("OpenSecureStore(%s) = %v, want %q", tc.dir, err, tc.want)
			}
			testutil.RequireUnchanged(t, before, root)
		})
	}
	t.Run("foreign owner", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root may take over any directory, so nothing is foreign to it")
		}
		if _, err := OpenSecureStore("/"); err == nil || !strings.Contains(err.Error(), "is owned by uid") {
			t.Fatalf("OpenSecureStore(/) = %v, want an \"is owned by uid\" refusal", err)
		}
	})
}

// TestSecureStoreClosedRefusesWrites: after Close a held-mode store refuses
// every write instead of falling back to writing by path.
func TestSecureStoreClosedRefusesWrites(t *testing.T) {
	for name, write := range blobWriters(t) {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "out")
			s, err := OpenSecureStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("second Close = %v, want nil", err)
			}
			requireBlobRefusal(t, write(s), "closed")
			requireEntries(t, root)
		})
	}
}

// TestWriteTreeReplacesSymlinkWithoutFollowing: a leftover tree blob that is a
// symlink (to a directory elsewhere), or an old tree holding a symlink to a
// directory elsewhere, is removed as the link itself: nothing below the link's
// target is deleted, and the new tree is written in its place.
func TestWriteTreeReplacesSymlinkWithoutFollowing(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range storeModes() {
		for name, plant := range map[string]func(tree, elsewhere string){
			"tree is a symlink": func(tree, elsewhere string) { mustSymlink(t, elsewhere, tree) },
			"tree holds a symlink": func(tree, elsewhere string) {
				testutil.MkdirMode(t, tree, 0o700)
				mustSymlink(t, elsewhere, filepath.Join(tree, "sub"))
			},
		} {
			t.Run(mode.name+" "+name, func(t *testing.T) {
				root := testutil.PrivateTempDir(t)
				elsewhere := testutil.MkdirMode(t, filepath.Join(t.TempDir(), "elsewhere"), 0o700)
				if err := os.WriteFile(filepath.Join(elsewhere, "keep"), []byte("precious"), 0o600); err != nil {
					t.Fatal(err)
				}
				testutil.MkdirMode(t, filepath.Join(root, "blobs"), 0o700)
				plant(filepath.Join(root, "blobs", "t"), elsewhere)
				before := testutil.Snapshot(t, elsewhere)
				ref, err := mode.open(t, root).WriteTree("t", src)
				if err != nil {
					t.Fatalf("WriteTree: %v", err)
				}
				testutil.RequireUnchanged(t, before, elsewhere)
				requireEntries(t, filepath.Join(root, filepath.FromSlash(ref)), "a")
				assertOwnerOnlyDir(t, filepath.Join(root, filepath.FromSlash(ref)))
			})
		}
	}
}

// TestWriteTreeClearReportsRealCauseAndPath: an old tree holding a
// non-empty subdirectory we may not list (0300) cannot be cleared; the error
// is the real cause (permission denied, not unlink's "is a directory") and
// names the path below blobs/ that failed, as os.RemoveAll's did.
func TestWriteTreeClearReportsRealCauseAndPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may list any directory")
	}
	src := t.TempDir()
	root := testutil.PrivateTempDir(t)
	sub := filepath.Join(root, "blobs", "t", "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	_, err := NewStore(root).WriteTree("t", src)
	requireBlobRefusal(t, err, `clear blob "blobs/t"`, "blobs/t/sub")
	if !errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("clear error = %v, want permission denied as the cause", err)
	}
}

// TestWriteTreeRefusesTreeReappearedAfterClear: a tree directory that
// reappears between the removal of the old tree and the creation of the new
// one (recreated at the blobTreeCleared seam, with content) is refused, not
// filled, and what it holds is left alone.
func TestWriteTreeRefusesTreeReappearedAfterClear(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := testutil.PrivateTempDir(t)
	prev := blobTreeCleared
	blobTreeCleared = func(tree string) {
		testutil.MkdirMode(t, tree, 0o700)
		if err := os.WriteFile(filepath.Join(tree, "planted"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { blobTreeCleared = prev })
	_, err := NewStore(root).WriteTree("t", src)
	requireBlobRefusal(t, err, `mkdir blob "blobs/t"`, "reappeared after it was cleared")
	requireEntries(t, filepath.Join(root, "blobs", "t"), "planted")
}

// mustSymlink creates the symlink link -> target.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
