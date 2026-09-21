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

// recorder is a Walk.Check that records every component it sees and refuses
// the one whose path ends in refuse.
type recorder struct {
	seen   []Component
	refuse string
}

var errRefused = errors.New("refused by check")

func (r *recorder) check(c Component) error {
	c.FD = -1 // only valid during the call
	r.seen = append(r.seen, c)
	if r.refuse != "" && strings.HasSuffix(c.Path, r.refuse) {
		return errRefused
	}
	return nil
}

// sameDir reports whether fd refers to the directory at path.
func sameDir(t *testing.T, fd int, path string) bool {
	t.Helper()
	var a, b unix.Stat_t
	if err := unix.Fstat(fd, &a); err != nil {
		t.Fatal(err)
	}
	if err := unix.Lstat(path, &b); err != nil {
		t.Fatal(err)
	}
	return a.Dev == b.Dev && a.Ino == b.Ino
}

// TestWalkChecksTopDown: Check sees every component in order with its path,
// Last only on the final one, and the base only when there are no components.
func TestWalkChecksTopDown(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a", "b")
	r := &recorder{}
	fd, err := Walk{Check: r.check}.OpenAt(openDir(t, root), root, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if !sameDir(t, fd, filepath.Join(root, "a", "b")) {
		t.Fatal("walk did not return the final directory")
	}
	want := []Component{{FD: -1, Path: filepath.Join(root, "a")}, {FD: -1, Path: filepath.Join(root, "a", "b"), Last: true}}
	if len(r.seen) != 2 || r.seen[0] != want[0] || r.seen[1] != want[1] {
		t.Fatalf("checked %+v, want %+v", r.seen, want)
	}

	r = &recorder{}
	base := openDir(t, root)
	fd2, err := Walk{Check: r.check}.OpenAt(base, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fd2 == base || !sameDir(t, fd2, root) {
		t.Fatal("walk without components must return a new descriptor of the base")
	}
	_ = unix.Close(fd2)
	if len(r.seen) != 1 || r.seen[0] != (Component{FD: -1, Path: root, Last: true}) {
		t.Fatalf("checked %+v, want only the base as last", r.seen)
	}
	if _, err := Fstat(base); err != nil {
		t.Fatalf("walk closed the caller's descriptor: %v", err)
	}
}

// TestWalkStopsAtFirstRefusal: a Check refusal is returned unwrapped and
// nothing below the refused component is opened or checked; an open failure
// is a *ComponentError naming the component.
func TestWalkStopsAtFirstRefusal(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a", "b", "c")
	r := &recorder{refuse: "/b"}
	_, err := Walk{Check: r.check}.OpenAt(openDir(t, root), root, []string{"a", "b", "c"})
	if err != errRefused {
		t.Fatalf("walk = %v, want the check's own error", err)
	}
	if len(r.seen) != 2 {
		t.Fatalf("checked %d components, want 2 (stop at b)", len(r.seen))
	}

	if err := os.Symlink("a", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	r = &recorder{}
	_, err = Walk{Check: r.check}.OpenAt(openDir(t, root), root, []string{"link", "b"})
	var ce *ComponentError
	if !errors.As(err, &ce) || !errors.Is(err, ErrSymlink) || ce.Name != "link" || ce.Path != filepath.Join(root, "link") {
		t.Fatalf("walk through a symlink = %#v, want a ComponentError for link", err)
	}
	if len(r.seen) != 0 {
		t.Fatalf("checked %+v through a refused component", r.seen)
	}
	if got, want := err.Error(), "open "+filepath.Join(root, "link")+": is a symlink"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

// TestWalkOpenRefusesSymlinkedBase: Open does not follow its base either.
func TestWalkOpenRefusesSymlinkedBase(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "real", "sub")
	if err := os.Symlink("real", filepath.Join(root, "base")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	_, err := Walk{}.Open("base", []string{"sub"})
	var ce *ComponentError
	if !errors.As(err, &ce) || !errors.Is(err, ErrSymlink) || ce.Name != "base" || ce.Path != "base" {
		t.Fatalf("Open through a symlinked base = %v, want a ComponentError for base", err)
	}
	fd, err := Walk{}.Open("real", []string{"sub"})
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
}

// TestWalkCreate: with Create every missing component is made exactly 0700
// and reported as Created; existing ones are neither reported as created nor
// chmod'ed. A relative path with leading ".." works (Split keeps them).
func TestWalkCreate(t *testing.T) {
	root := t.TempDir()
	testutil.MkdirMode(t, filepath.Join(root, "old"), 0o755)
	mkdirs(t, root, "cwd")
	t.Chdir(filepath.Join(root, "cwd"))
	base, parts := Split("../old/new/deeper")
	r := &recorder{}
	var fd int
	var err error
	testutil.WithUmask(0o077, func() { fd, err = Walk{Create: true, Check: r.check}.Open(base, parts) })
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	var created []bool
	for _, c := range r.seen {
		created = append(created, c.Created)
	}
	if len(created) != 4 || created[0] || created[1] || !created[2] || !created[3] {
		t.Fatalf("created flags = %v, want [false false true true]", created)
	}
	if got := modeOf(t, filepath.Join(root, "old")); got != 0o755 {
		t.Fatalf("existing mode = %v, want 0755 untouched", got)
	}
	for _, dir := range []string{"old/new", "old/new/deeper"} {
		if got := modeOf(t, filepath.Join(root, dir)); got != 0o700 {
			t.Fatalf("%s mode = %v, want 0700", dir, got)
		}
	}
}

// TestWalkStaysOnTheCheckedDirectory is the reason for the descriptor walk:
// once a component was checked, swapping its path for a symlink (here from
// inside Check, the worst possible moment) cannot redirect the rest of the
// walk. It continues in, and returns, the directory that was checked.
func TestWalkStaysOnTheCheckedDirectory(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a", "b")
	mkdirs(t, root, "evil", "b")
	swap := func(c Component) error {
		if c.Path != filepath.Join(root, "a") {
			return nil
		}
		if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "moved")); err != nil {
			return err
		}
		return os.Symlink("evil", filepath.Join(root, "a"))
	}
	fd, err := Walk{Check: swap}.OpenAt(openDir(t, root), root, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if !sameDir(t, fd, filepath.Join(root, "moved", "b")) {
		t.Fatal("the walk followed the swapped-in symlink instead of the checked directory")
	}
}

// TestWalkNeedsOnlySearchPermission: where SearchOnly holds, a directory the
// caller may search but not read (0311) can be walked through, fstat'ed by
// Check and returned, as an lstat walk allowed; OpenFollowingDir likewise. A
// directory without search permission (0600) still stops the walk. The
// directories are the test user's own, so no root is needed; root bypasses
// the permissions and is skipped.
func TestWalkNeedsOnlySearchPermission(t *testing.T) {
	if !SearchOnly {
		t.Skip("this platform's walk needs read permission (search_other.go)")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	mkdirs(t, root, "a", "b")
	mkdirs(t, root, "closed", "x")
	for dir, mode := range map[string]os.FileMode{"a": 0o311, "a/b": 0o311, "closed": 0o600} {
		if err := os.Chmod(filepath.Join(root, dir), mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(root, "a"), 0o700)
		_ = os.Chmod(filepath.Join(root, "closed"), 0o700)
	})
	check := func(c Component) error { _, err := Fstat(c.FD); return err }
	fd, err := Walk{Check: check}.OpenAt(openDir(t, root), root, []string{"a", "b"})
	if err != nil {
		t.Fatalf("walk through 0311 directories = %v, want success", err)
	}
	_ = unix.Close(fd)
	fd, err = OpenFollowingDir(filepath.Join(root, "a", "b"))
	if err != nil {
		t.Fatalf("OpenFollowingDir of a 0311 directory = %v", err)
	}
	_ = unix.Close(fd)
	if _, err := (Walk{}).OpenAt(openDir(t, root), root, []string{"closed", "x"}); !errors.Is(err, unix.EACCES) {
		t.Fatalf("walk below a directory without search permission = %v, want EACCES", err)
	}
}

// TestWalkRefusesInvalidComponents: a component that is not exactly one name
// is refused rather than resolved.
func TestWalkRefusesInvalidComponents(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a", "b")
	for _, parts := range [][]string{{"a/b"}, {""}, {"a", "."}} {
		if _, err := (Walk{}).OpenAt(openDir(t, root), root, parts); !errors.Is(err, ErrInvalidComponent) {
			t.Errorf("walk %q = %v, want ErrInvalidComponent", parts, err)
		}
	}
}

// TestWalkDoesNotLeakDescriptors: successful and refused walks close every
// descriptor except the one they return (Linux only: counts /proc/self/fd).
func TestWalkDoesNotLeakDescriptors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("counts /proc/self/fd")
	}
	root := t.TempDir()
	mkdirs(t, root, "a", "b", "c")
	if err := os.Symlink("a", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	walks := func() {
		fd, err := Walk{}.Open("/", mustSplit(root, "a", "b", "c"))
		if err != nil {
			t.Fatal(err)
		}
		_ = unix.Close(fd)
		_, _ = Walk{}.Open("/", mustSplit(root, "a", "missing"))
		_, _ = Walk{}.Open("/", mustSplit(root, "link", "b"))
		_, _ = Walk{Check: func(Component) error { return errRefused }}.Open("/", mustSplit(root, "a", "b"))
		_, _ = Walk{Create: true, Check: func(c Component) error {
			if c.Last {
				return errRefused
			}
			return nil
		}}.Open("/", mustSplit(root, "a", "new"))
	}
	walks()
	before := openFDs(t)
	for range 50 {
		walks()
	}
	if after := openFDs(t); after != before {
		t.Fatalf("open descriptors %d -> %d after 50 rounds of walks", before, after)
	}
}

// mkdirs creates root/names[0]/names[1]/... one 0700 level at a time.
func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for i := range names {
		dir := filepath.Join(append([]string{root}, names[:i+1]...)...)
		if _, err := os.Lstat(dir); err != nil {
			testutil.MkdirMode(t, dir, 0o700)
		}
	}
}

// mustSplit returns the components of root joined with more, below "/".
func mustSplit(root string, more ...string) []string {
	_, parts := Split(filepath.Join(append([]string{root}, more...)...))
	return parts
}

// openFDs counts the process's open descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
