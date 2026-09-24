package seal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// writeRecipientsFile writes a recipients file at dir/name containing
// content, with the given permission bits, and returns its full path — the
// recipients-file sibling of identities_test.go's writeIdentityFile.
func writeRecipientsFile(t *testing.T, dir, name, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write recipients file: %v", err)
	}
	// os.WriteFile's mode is narrowed by umask on creation; force it exactly,
	// since these tests are about the mode LoadRecipientsFile sees.
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod recipients file: %v", err)
	}
	return path
}

func TestLoadRecipientsFileAcceptsOwnedRegularFile(t *testing.T) {
	line := mustHybridRecipientLine(t)
	dir := t.TempDir()
	path := writeRecipientsFile(t, dir, "recipients", line+"\n", 0o600)

	lines, err := LoadRecipientsFile(path)
	if err != nil {
		t.Fatalf("LoadRecipientsFile: %v", err)
	}
	if len(lines) == 0 || lines[0] != line {
		t.Fatalf("got %v, want first line %q", lines, line)
	}
}

// TestLoadRecipientsFileAcceptsGroupAndOtherReadableFile proves the
// narrower rule ErrRecipientsFileWritable's doc comment describes: unlike
// an identity file (refused on ANY group/other bit), a recipients file
// holding only public keys is fine to be group/other READABLE — only a
// WRITE bit for group or other is refused.
func TestLoadRecipientsFileAcceptsGroupAndOtherReadableFile(t *testing.T) {
	line := mustHybridRecipientLine(t)
	dir := t.TempDir()
	path := writeRecipientsFile(t, dir, "recipients", line+"\n", 0o644)

	if _, err := LoadRecipientsFile(path); err != nil {
		t.Fatalf("LoadRecipientsFile rejected a 0644 (group/other readable, not writable) file: %v", err)
	}
}

func TestLoadRecipientsFileRefusesGroupWritableFile(t *testing.T) {
	line := mustHybridRecipientLine(t)
	dir := t.TempDir()
	path := writeRecipientsFile(t, dir, "recipients", line+"\n", 0o664)

	_, err := LoadRecipientsFile(path)
	if !errors.Is(err, ErrRecipientsFileWritable) {
		t.Fatalf("got %v, want ErrRecipientsFileWritable", err)
	}
}

// TestLoadRecipientsFileRefusesOtherWritableFile reproduces task ce2's
// probe (B): a plain mode-666 (world-writable) regular recipients file is
// refused, closing the vulnerability where such a file was silently
// accepted and its extra recipient lines unioned in.
func TestLoadRecipientsFileRefusesOtherWritableFile(t *testing.T) {
	line := mustHybridRecipientLine(t)
	dir := t.TempDir()
	path := writeRecipientsFile(t, dir, "recipients", line+"\n", 0o666)

	_, err := LoadRecipientsFile(path)
	if !errors.Is(err, ErrRecipientsFileWritable) {
		t.Fatalf("got %v, want ErrRecipientsFileWritable", err)
	}
}

// TestLoadRecipientsFileRefusesSymlink reproduces task ce2's probe (A): a
// recipients file reached through a symlink is refused, closing the
// vulnerability where an attacker-planted symlink to a malicious file was
// silently followed.
func TestLoadRecipientsFileRefusesSymlink(t *testing.T) {
	line := mustHybridRecipientLine(t)
	dir := t.TempDir()
	realPath := writeRecipientsFile(t, dir, "recipients-real", line+"\n", 0o644)
	linkPath := filepath.Join(dir, "recipients-link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := LoadRecipientsFile(linkPath)
	if !errors.Is(err, ErrRecipientsFileSymlink) {
		t.Fatalf("got %v, want ErrRecipientsFileSymlink", err)
	}
}

func TestLoadRecipientsFileRefusesNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "recipients")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := LoadRecipientsFile(sub)
	if !errors.Is(err, ErrRecipientsFileNotRegular) {
		t.Fatalf("got %v, want ErrRecipientsFileNotRegular", err)
	}
}

// TestLoadRecipientsFileMissingIsErrNotExist proves a missing file is
// reported, not swallowed, so the caller (internal/cli) can apply its own
// policy: silently absent for the ambient default, an error for an
// explicit -recipients-file.
func TestLoadRecipientsFileMissingIsErrNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadRecipientsFile(filepath.Join(dir, "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want errors.Is(err, os.ErrNotExist)", err)
	}
}

func TestLoadRecipientsFileNeverEchoesContent(t *testing.T) {
	line := mustHybridRecipientLine(t)
	dir := t.TempDir()
	path := writeRecipientsFile(t, dir, "recipients", line+"\n", 0o666)

	_, err := LoadRecipientsFile(path)
	if err == nil {
		t.Fatal("LoadRecipientsFile accepted a world-writable file")
	}
	if strings.Contains(err.Error(), line) {
		t.Fatalf("error echoes the recipients file's content: %v", err)
	}
}

// TestCheckRecipientsOwnerAndModeRefusesForeignOwner exercises the
// ownership rule directly with a fabricated safepath.Info, the technique
// identities.go's TestCheckOwnerAndModeRefusesForeignOwner uses for the
// same reason: a test cannot chown a real file to a uid it does not own
// without being root.
func TestCheckRecipientsOwnerAndModeRefusesForeignOwner(t *testing.T) {
	me := unix.Geteuid()
	foreign := me + 1
	info := safepath.Info{Mode: unix.S_IFREG | 0o644, UID: uint32(foreign)}

	err := recipientsFile.checkOwnerAndMode(info, "/fake/recipients", me)
	if !errors.Is(err, ErrRecipientsFileNotOwned) {
		t.Fatalf("got %v, want ErrRecipientsFileNotOwned", err)
	}
}

func TestCheckRecipientsOwnerAndModeAcceptsReadableNotWritable(t *testing.T) {
	me := unix.Geteuid()
	info := safepath.Info{Mode: unix.S_IFREG | 0o644, UID: uint32(me)}
	if err := recipientsFile.checkOwnerAndMode(info, "/fake/recipients", me); err != nil {
		t.Fatalf("recipientsFile.checkOwnerAndMode rejected an own 0644 file: %v", err)
	}
}

func TestCheckRecipientsOwnerAndModeRefusesWritable(t *testing.T) {
	me := unix.Geteuid()
	info := safepath.Info{Mode: unix.S_IFREG | 0o606, UID: uint32(me)}
	err := recipientsFile.checkOwnerAndMode(info, "/fake/recipients", me)
	if !errors.Is(err, ErrRecipientsFileWritable) {
		t.Fatalf("got %v, want ErrRecipientsFileWritable", err)
	}
}
