package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/snonux/gonf/internal/testutil"
)

// openDir opens path as a directory descriptor closed at the end of the test.
func openDir(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

// modeOf is the chmod bits of path, without following a symlink.
func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode() & (os.ModePerm | os.ModeSticky | os.ModeSetgid | os.ModeSetuid)
}

// fixture is a directory with a subdirectory, a regular file, a FIFO, a
// symlink to the subdirectory, a symlink to the file and a dangling symlink.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testutil.MkdirMode(t, filepath.Join(root, "dir"), 0o700)
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"dirlink": "dir", "filelink": "file", "dangling": "nowhere"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		path, base string
		parts      []string
	}{
		{"/", "/", nil},
		{".", ".", nil},
		{"", ".", nil},
		{"/a/b/", "/", []string{"a", "b"}},
		{"//a//./b", "/", []string{"a", "b"}},
		{"a/../b", ".", []string{"b"}},
		{"../../out", ".", []string{"..", "..", "out"}},
		{"/../a", "/", []string{"a"}},
	} {
		base, parts := Split(tc.path)
		if base != tc.base || strings.Join(parts, "|") != strings.Join(tc.parts, "|") {
			t.Errorf("Split(%q) = %q, %q; want %q, %q", tc.path, base, parts, tc.base, tc.parts)
		}
	}
}

// TestOpenDirAtRefusals: every name that is not a real directory is refused
// without following anything, and a symlink is always ErrSymlink (whatever
// errno the platform gives), a regular file is not.
func TestOpenDirAtRefusals(t *testing.T) {
	root := fixture(t)
	parent := openDir(t, root)
	for _, tc := range []struct {
		name string
		want error
	}{
		{"dirlink", ErrSymlink},
		{"filelink", ErrSymlink},
		{"dangling", ErrSymlink},
		{"file", unix.ENOTDIR},
		{"fifo", unix.ENOTDIR},
		{"missing", unix.ENOENT},
		{"", ErrInvalidComponent},
		{".", ErrInvalidComponent},
		{"dir/x", ErrInvalidComponent},
	} {
		fd, err := OpenDirAt(parent, tc.name)
		if err == nil {
			_ = unix.Close(fd)
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("OpenDirAt(%q) = %v, want %v", tc.name, err, tc.want)
		}
		if tc.want != ErrSymlink && errors.Is(err, ErrSymlink) {
			t.Errorf("OpenDirAt(%q) reported a symlink", tc.name)
		}
	}
	fd, err := OpenDirAt(parent, "dir")
	if err != nil {
		t.Fatalf("OpenDirAt(dir) = %v", err)
	}
	_ = unix.Close(fd)
}

func TestOpenBase(t *testing.T) {
	root := fixture(t)
	t.Chdir(root)
	for _, base := range []string{"/", ".", "dir", ".."} {
		fd, err := OpenBase(base)
		if err != nil {
			t.Fatalf("OpenBase(%q) = %v", base, err)
		}
		_ = unix.Close(fd)
	}
	for base, want := range map[string]error{
		"dirlink":  ErrSymlink,
		"dangling": ErrSymlink,
		"file":     unix.ENOTDIR,
		"missing":  unix.ENOENT,
		"dir/.":    ErrInvalidComponent,
		root:       ErrInvalidComponent,
		"":         ErrInvalidComponent,
	} {
		if _, err := OpenBase(base); !errors.Is(err, want) {
			t.Errorf("OpenBase(%q) = %v, want %v", base, err, want)
		}
	}
}

// TestOpenRegularAt: only a regular file is returned; a FIFO is refused
// without blocking on it, a symlink (even to a regular file) without following.
func TestOpenRegularAt(t *testing.T) {
	root := fixture(t)
	parent := openDir(t, root)
	file, err := OpenRegularAt(parent, "file", "the/name")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8)
	n, _ := file.Read(data)
	_ = file.Close()
	if string(data[:n]) != "data" || file.Name() != "the/name" {
		t.Fatalf("read %q from %s, want data from the/name", data[:n], file.Name())
	}
	for name, want := range map[string]error{
		"filelink": ErrSymlink,
		"dangling": ErrSymlink,
		"dirlink":  ErrSymlink,
		"fifo":     ErrNotRegular,
		"dir":      ErrNotRegular,
		"missing":  unix.ENOENT,
		"..":       ErrNotRegular,
		"":         ErrInvalidComponent,
		"dir/file": ErrInvalidComponent,
	} {
		if f, err := OpenRegularAt(parent, name, name); !errors.Is(err, want) || f != nil {
			t.Errorf("OpenRegularAt(%q) = %v, %v; want nil, %v", name, f, err, want)
		}
	}
}

// TestOpenRegularAtDoesNotLeakDescriptors: a refused open closes what it
// opened (a directory or FIFO it opened and then found not regular), and a
// successful one hands its only descriptor to the *os.File (Linux only: counts
// /proc/self/fd).
func TestOpenRegularAtDoesNotLeakDescriptors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("counts /proc/self/fd")
	}
	parent := openDir(t, fixture(t))
	opens := func() {
		for _, name := range []string{"dir", "fifo", "filelink", "missing"} {
			if f, err := OpenRegularAt(parent, name, name); err == nil {
				_ = f.Close()
				t.Fatalf("OpenRegularAt(%q) succeeded", name)
			}
		}
		f, err := OpenRegularAt(parent, "file", "file")
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	opens()
	before := openFDs(t)
	for range 50 {
		opens()
	}
	if after := openFDs(t); after != before {
		t.Fatalf("open descriptors %d -> %d after 50 rounds of OpenRegularAt", before, after)
	}
}

// makeRivalWin returns a MkdirFunc that behaves as if another process created
// the directory first: it creates <parent>/<name> with mode, as the rival
// would, and then asks the real mkdirat, which fails with EEXIST.
func makeRivalWin(t *testing.T, parent string, mode os.FileMode) MkdirFunc {
	return func(fd int, name string, perm uint32) error {
		rival := filepath.Join(parent, name)
		testutil.MkdirMode(t, rival, mode)
		return unix.Mkdirat(fd, name, perm)
	}
}

// TestOpenOrCreateDirAtLosingTheRace: when another process creates the
// directory between the failed open and our mkdir, that directory is somebody
// else's: created is false, so no caller chmods it (the rival's 0755 stays),
// and the caller's verification decides.
func TestOpenOrCreateDirAtLosingTheRace(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	testutil.MkdirMode(t, parent, 0o755)
	fd, created, err := OpenOrCreateDirAt(openDir(t, parent), "x", makeRivalWin(t, parent, 0o755))
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	if created {
		t.Fatal("created = true after losing the creation race, want false")
	}
	if got := modeOf(t, filepath.Join(parent, "x")); got != 0o755 {
		t.Fatalf("raced-in directory mode = %v, want the rival's 0755 untouched", got)
	}
}

// TestOpenOrCreateDirAtModes: a directory it creates is exactly 0700 whatever
// the umask (0277 would otherwise leave an unusable 0500), and an existing one
// is opened as it is, never chmod'ed.
func TestOpenOrCreateDirAtModes(t *testing.T) {
	for _, mask := range []int{0, 0o002, 0o022, 0o077, 0o277} {
		parent := t.TempDir()
		var fd int
		var created bool
		var err error
		testutil.WithUmask(mask, func() { fd, created, err = OpenOrCreateDirAt(openDir(t, parent), "new", nil) })
		if err != nil || !created {
			t.Fatalf("umask %04o: OpenOrCreateDirAt = %v, created %v", mask, err, created)
		}
		_ = unix.Close(fd)
		if got := modeOf(t, filepath.Join(parent, "new")); got != 0o700 {
			t.Fatalf("umask %04o: created mode = %v, want 0700", mask, got)
		}
	}
	parent := t.TempDir()
	testutil.MkdirMode(t, filepath.Join(parent, "old"), 0o755)
	fd, created, err := OpenOrCreateDirAt(openDir(t, parent), "old", nil)
	if err != nil || created {
		t.Fatalf("existing: OpenOrCreateDirAt = %v, created %v", err, created)
	}
	_ = unix.Close(fd)
	if got := modeOf(t, filepath.Join(parent, "old")); got != 0o755 {
		t.Fatalf("existing mode = %v, want 0755 untouched", got)
	}
}

// TestOpenOrCreateDirAtRefusals: a symlink or a file in the way is refused
// and nothing is created through it; a mkdir failure is returned as it is.
func TestOpenOrCreateDirAtRefusals(t *testing.T) {
	root := fixture(t)
	parent := openDir(t, root)
	for name, want := range map[string]error{"dirlink": ErrSymlink, "dangling": ErrSymlink, "file": unix.ENOTDIR} {
		if _, _, err := OpenOrCreateDirAt(parent, name, nil); !errors.Is(err, want) {
			t.Errorf("OpenOrCreateDirAt(%q) = %v, want %v", name, err, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "nowhere")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling symlink target was created: %v", err)
	}
	failing := func(int, string, uint32) error { return unix.EROFS }
	if _, _, err := OpenOrCreateDirAt(parent, "new", failing); !errors.Is(err, unix.EROFS) {
		t.Fatalf("OpenOrCreateDirAt with a failing mkdir = %v, want EROFS", err)
	}
}
