package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// DefaultDir is the directory FileProvider reads from when Dir is empty,
// relative to the working directory (the recipe checkout).
const DefaultDir = "secrets"

// readChunk is how much FileProvider reads between context checks.
const readChunk = 32 << 10

// errRootMissing marks a missing Dir, so openError can tell it apart from a
// missing secret below it.
var errRootMissing = errors.New("secrets directory missing")

// errRootVanished marks a Dir that was opened fine but was removed, renamed
// or replaced before the lookup below it finished, so the resulting ENOENT
// is a store failure and not a missing secret.
var errRootVanished = errors.New("secrets directory vanished")

// FileProvider reads each secret from the regular file <Dir>/<ref> and
// returns its bytes exactly. It is the provider api.MustSecret and
// api.OptionalSecret use unless the consumer configures another one, and its
// messages are the ones those helpers have always reported.
//
// A leading slash in the reference is accepted for compatibility with the Rex
// convention: "/var/nsd/key" reads secrets/var/nsd/key, not /var/nsd/key.
// Leading backslashes are stripped as well, as the api helpers always did
// (`\var/nsd/key` reads the same file); a backslash anywhere else is an
// ordinary name character, as it is in Dir. References may not escape Dir.
//
// Errors:
//   - ErrNotFound: the secret, or a directory on its way below Dir, is absent
//     while Dir itself is still in place;
//   - ErrUnavailable: Dir itself is absent (gonf run from the wrong working
//     directory, the tree renamed or removed) or cannot be opened or
//     searched, or Dir is misconfigured — never "not found", so an optional
//     lookup cannot silently drop every secret. That includes Dir being
//     removed, renamed or replaced after it was opened, which the lookup
//     below it sees as a missing entry (see rootVanished). Limit: when Dir
//     is itself a mount point and the filesystem is unmounted, the empty
//     mount-point directory remains, so every secret below it reads as
//     ErrNotFound; this provider cannot tell an empty store from an
//     unmounted one;
//   - ErrInvalid: empty or escaping reference, a symlink or a non-directory
//     on the path, or a final component that is not a regular file;
//   - ErrUnreadable: any other open failure below Dir (permission denied) or
//     a read failure.
//
// A context that is already done is refused before anything is opened; a
// context that becomes done is re-checked between read chunks.
type FileProvider struct {
	// Dir is a single path component relative to the working directory, or
	// "." (DefaultDir when empty). It is opened without following a symlink;
	// ".." is refused so the store stays inside the checkout.
	Dir string
}

// rootError marks any other failure of Dir itself (as opposed to a secret or
// directory below it), so openError can report it as a store-level failure.
type rootError struct{ err error }

// Resolve implements Provider.
func (p FileProvider) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, canceled(ref, err)
	}
	dir, err := p.dir(ref)
	if err != nil {
		return nil, err
	}
	clean, err := cleanRef(ref)
	if err != nil {
		return nil, err
	}
	fullPath := filepath.Join(dir, clean)
	file, err := openFile(dir, clean)
	if err != nil {
		return nil, openError(ref, dir, fullPath, err)
	}
	defer func() { _ = file.Close() }()
	data, err := readAll(ctx, file)
	if err != nil {
		if ctx.Err() != nil {
			return nil, canceled(ref, ctx.Err())
		}
		return nil, &Error{Kind: ErrUnreadable, Ref: ref, Err: err,
			Msg: fmt.Sprintf("read secret %q: %v", string(ref), err)}
	}
	return data, nil
}

// Error returns the underlying failure's message.
func (e rootError) Error() string { return e.err.Error() }

// Unwrap exposes the underlying failure to errors.Is/As.
func (e rootError) Unwrap() error { return e.err }

// dir returns the effective directory, refusing a misconfigured one as
// ErrUnavailable: a configuration error of the store, not of one secret.
// Only a single component (no filepath.Separator, the rule of
// internal/safepath; a backslash is an ordinary name character on unix) or
// "." is accepted — "." means the operator chose
// the whole working directory — and ".." is refused so the store stays
// inside the checkout. (OpenBase would refuse a multi-component Dir anyway.)
func (p FileProvider) dir(ref Ref) (string, error) {
	dir := p.Dir
	if dir == "" {
		dir = DefaultDir
	}
	if strings.ContainsRune(dir, filepath.Separator) {
		return "", &Error{Kind: ErrUnavailable, Ref: ref,
			Msg: fmt.Sprintf("secret %q: file provider directory %q must be a single path component", string(ref), dir)}
	}
	if dir == ".." {
		return "", &Error{Kind: ErrUnavailable, Ref: ref,
			Msg: fmt.Sprintf("secret %q: file provider directory must not be %q", string(ref), dir)}
	}
	return dir, nil
}

// cleanRef strips leading slashes (the Rex convention) and backslashes (as
// the api helpers always have: `\a/b` reads secrets/a/b) and cleans the path,
// refusing an empty reference and anything that would leave the directory.
func cleanRef(ref Ref) (string, error) {
	trimmed := strings.TrimLeft(string(ref), "/\\")
	if trimmed == "" {
		return "", &Error{Kind: ErrInvalid, Ref: ref, Msg: "secret path must not be empty"}
	}
	clean := filepath.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", &Error{Kind: ErrInvalid, Ref: ref, Msg: fmt.Sprintf("invalid secret path %q", string(ref))}
	}
	return clean, nil
}

// openRoot opens dir and checks that it can be searched, so a secrets/ the
// operator cannot use is reported as a root (store) failure rather than as
// an "unreadable secret" at its first child. What OpenBase's open itself
// requires differs per platform (internal/safepath searchFlag):
//   - Linux, O_PATH: no permission at all, so the open always succeeds;
//   - FreeBSD, O_SEARCH: search (x) permission, so the open already fails
//     with EACCES and the check below is redundant there;
//   - OpenBSD, NetBSD, macOS, O_RDONLY: read (r) permission, so a
//     readable but unsearchable directory (r--) opens fine, while a
//     search-only one (--x) fails the open with EACCES and is refused as a
//     root failure too, as it always was.
//
// The faccessat(X_OK) is therefore what catches an unsearchable secrets/ on
// Linux and on the O_RDONLY platforms; it runs everywhere for one behaviour.
// (It checks the real, not the effective, IDs; gonf is not setuid.)
func openRoot(dir string) (int, error) {
	rootFD, err := safepath.OpenBase(dir)
	if errors.Is(err, unix.ENOENT) {
		return -1, errRootMissing
	}
	if err != nil {
		return -1, rootError{err}
	}
	if err := unix.Faccessat(rootFD, ".", unix.X_OK, 0); err != nil {
		_ = unix.Close(rootFD)
		return -1, rootError{err}
	}
	return rootFD, nil
}

// openFile opens dir/clean with the shared descriptor walk of
// internal/safepath: dir is opened relative to the working directory without
// following it, every directory below it relative to its parent's descriptor
// with O_NOFOLLOW, and the secret itself O_NOFOLLOW|O_NONBLOCK and only when
// it is a regular file. An attacker can therefore not race a checked pathname
// into a symlink outside the controller-owned secrets tree. It also cannot
// climb out of it: the walk would follow a ".." component, but cleanRef has
// already refused any path that keeps one after cleaning. Nothing is created,
// and ownership and modes are not checked: the secrets tree is the
// operator's own checkout.
func openFile(dir, clean string) (*os.File, error) {
	rootFD, err := openRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(rootFD) }()
	return openBelowRoot(rootFD, dir, clean)
}

// openBelowRoot opens clean below the already opened and checked Dir
// (rootFD). A missing entry (ENOENT) is re-examined with rootVanished: when
// Dir itself went away meanwhile, the result is errRootVanished instead of
// the ENOENT, so a store removed mid-lookup never reads as "not found".
func openBelowRoot(rootFD int, dir, clean string) (*os.File, error) {
	parts := strings.Split(clean, string(filepath.Separator))
	dirs, name := parts[:len(parts)-1], parts[len(parts)-1]
	file, err := openWalk(rootFD, dir, dirs, name, filepath.Join(dir, clean))
	if errors.Is(err, unix.ENOENT) && rootVanished(rootFD, dir) {
		return nil, errRootVanished
	}
	return file, err
}

// openWalk walks dirs below rootFD and opens the regular file name there.
func openWalk(rootFD int, dir string, dirs []string, name, fullPath string) (*os.File, error) {
	dirFD, err := safepath.Walk{}.OpenAt(rootFD, dir, dirs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	return safepath.OpenRegularAt(dirFD, name, fullPath)
}

// rootVanished reports whether the opened Dir (rootFD) is no longer the
// directory dir in the working directory: it was removed (no links left),
// or dir now names nothing or a different object (renamed or replaced).
// It is checked after an ENOENT, so it catches a root that went away at any
// point before that failure was observed — including a lazy unmount of a
// filesystem mounted on Dir during the lookup, since dir then names the
// mount point, a different device. It cannot see an unmount that happened
// before Dir was opened (see FileProvider).
func rootVanished(rootFD int, dir string) bool {
	var held, named unix.Stat_t
	if err := unix.Fstat(rootFD, &held); err != nil || held.Nlink == 0 {
		return true
	}
	if err := unix.Fstatat(unix.AT_FDCWD, dir, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return true
	}
	return held.Dev != named.Dev || held.Ino != named.Ino
}

// openError classifies and words a failure of openFile. A symlink anywhere on
// the path is reported as such; so is ENOTDIR, a component that is a regular
// file where a directory is needed, which the walk has always reported with
// the symlink wording (and ELOOP, which Linux gives for a symlink opened
// O_NOFOLLOW, is kept for safety although safepath already diagnoses it as
// safepath.ErrSymlink). Other errors keep the bare cause after the secret's
// name, without the path the walk adds. These are the messages MustSecret
// and OptionalSecret reported before providers existed, kept verbatim; only
// their kind depends on where the failure happened. A missing or vanished
// Dir carries no cause on purpose: wrapping ENOENT would make errors.Is(err, fs.ErrNotExist)
// true for an unavailable store. Any other failure of Dir itself (permission
// denied) is ErrUnavailable, a store-level failure, with the historical
// "open secret" wording.
func openError(ref Ref, dir, fullPath string, err error) error {
	var root rootError
	isRoot := errors.As(err, &root)
	switch {
	case errors.Is(err, errRootMissing):
		return &Error{Kind: ErrUnavailable, Ref: ref,
			Msg: fmt.Sprintf("secret %q: secrets directory %q not found in the working directory", string(ref), dir)}
	case errors.Is(err, errRootVanished):
		return &Error{Kind: ErrUnavailable, Ref: ref,
			Msg: fmt.Sprintf("secret %q: secrets directory %q was removed or replaced during the lookup", string(ref), dir)}
	case errors.Is(err, safepath.ErrSymlink), errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		// Also for Dir itself: a symlinked secrets/ is refused as unsafe.
		return &Error{Kind: ErrInvalid, Ref: ref, Err: err, Msg: fmt.Sprintf("secret path %q contains a symlink", fullPath)}
	case isRoot:
		// Every other failure of Dir — checked before ENOENT, so a root
		// that vanishes between open and check is never "not found".
		return rootOpenError(ref, root.err)
	case errors.Is(err, unix.ENOENT):
		return &Error{Kind: ErrNotFound, Ref: ref, Msg: fmt.Sprintf("secret %q is missing", string(ref))}
	case errors.Is(err, safepath.ErrNotRegular):
		return &Error{Kind: ErrInvalid, Ref: ref, Err: err, Msg: fmt.Sprintf("secret %q is not a regular file", string(ref))}
	}
	var ce *safepath.ComponentError
	if errors.As(err, &ce) {
		err = ce.Err
	}
	return &Error{Kind: ErrUnreadable, Ref: ref, Err: err, Msg: fmt.Sprintf("open secret %q: %v", string(ref), err)}
}

// rootOpenError is the ErrUnavailable for a failure of Dir itself, with the
// historical "open secret" wording.
func rootOpenError(ref Ref, err error) error {
	var ce *safepath.ComponentError
	if errors.As(err, &ce) {
		err = ce.Err
	}
	return &Error{Kind: ErrUnavailable, Ref: ref, Err: err, Msg: fmt.Sprintf("open secret %q: %v", string(ref), err)}
}

// readAll reads file to EOF, checking ctx between chunks so a cancelled
// resolution stops even on a large or slow file.
func readAll(ctx context.Context, file *os.File) ([]byte, error) {
	var buf bytes.Buffer
	chunk := make([]byte, readChunk)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := file.Read(chunk)
		buf.Write(chunk[:n])
		if errors.Is(err, io.EOF) {
			return buf.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}
