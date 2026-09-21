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

// FileProvider reads each secret from the regular file <Dir>/<ref> and
// returns its bytes exactly. It is the provider api.MustSecret and
// api.OptionalSecret use unless the consumer configures another one, and its
// messages are the ones those helpers have always reported.
//
// A leading slash in the reference is accepted for compatibility with the Rex
// convention: "/var/nsd/key" reads secrets/var/nsd/key, not /var/nsd/key.
// References may not escape Dir.
//
// Errors:
//   - ErrNotFound: the secret, or a directory on its way below Dir, is absent;
//   - ErrUnavailable: Dir itself is absent (gonf run from the wrong working
//     directory, the tree renamed or unmounted) — never "not found", so an
//     optional lookup cannot silently drop every secret;
//   - ErrInvalid: empty or escaping reference, a symlink or a non-directory
//     on the path, or a final component that is not a regular file;
//   - ErrUnreadable: any other open failure (permission denied) or a read
//     failure.
//
// A context that is already done is refused before anything is opened; a
// context that becomes done is re-checked between read chunks.
type FileProvider struct {
	// Dir is a single path component relative to the working directory, or
	// "." (DefaultDir when empty). It is opened without following a symlink;
	// ".." is refused so the store stays inside the checkout.
	Dir string
}

// Resolve implements Provider.
func (p FileProvider) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, canceled(ref, err)
	}
	dir := p.Dir
	if dir == "" {
		dir = DefaultDir
	}
	if strings.ContainsAny(dir, `/\`) {
		// OpenBase would refuse it anyway; say it is a configuration error
		// rather than a per-secret one.
		return nil, &Error{Kind: ErrUnavailable, Ref: ref,
			Msg: fmt.Sprintf("secret %q: file provider directory %q must be a single path component", string(ref), dir)}
	}
	if dir == ".." {
		// Also a configuration error: the store must stay inside the
		// checkout ("." is allowed; the operator chose the whole working
		// directory as the root).
		return nil, &Error{Kind: ErrUnavailable, Ref: ref,
			Msg: fmt.Sprintf("secret %q: file provider directory must not be %q", string(ref), dir)}
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

// cleanRef strips leading slashes (the Rex convention) and cleans the path,
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

// errRootMissing marks a missing Dir, so openError can tell it apart from a
// missing secret below it.
var errRootMissing = errors.New("secrets directory missing")

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
	parts := strings.Split(clean, string(filepath.Separator))
	dirs, name := parts[:len(parts)-1], parts[len(parts)-1]
	rootFD, err := safepath.OpenBase(dir)
	if errors.Is(err, unix.ENOENT) {
		return nil, errRootMissing
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(rootFD) }()
	dirFD, err := safepath.Walk{}.OpenAt(rootFD, dir, dirs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	return safepath.OpenRegularAt(dirFD, name, filepath.Join(dir, clean))
}

// openError classifies and words a failure of openFile. A symlink anywhere on
// the path is reported as such; so is ENOTDIR, a component that is a regular
// file where a directory is needed, which the walk has always reported with
// the symlink wording (and ELOOP, which Linux gives for a symlink opened
// O_NOFOLLOW, is kept for safety although safepath already diagnoses it as
// safepath.ErrSymlink). Other errors keep the bare cause after the secret's
// name, without the path the walk adds. These are the messages MustSecret
// and OptionalSecret reported before providers existed, kept verbatim.
func openError(ref Ref, dir, fullPath string, err error) error {
	switch {
	case errors.Is(err, errRootMissing):
		return &Error{Kind: ErrUnavailable, Ref: ref, Err: unix.ENOENT,
			Msg: fmt.Sprintf("secret %q: secrets directory %q not found in the working directory", string(ref), dir)}
	case errors.Is(err, unix.ENOENT):
		return &Error{Kind: ErrNotFound, Ref: ref, Msg: fmt.Sprintf("secret %q is missing", string(ref))}
	case errors.Is(err, safepath.ErrSymlink), errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		return &Error{Kind: ErrInvalid, Ref: ref, Err: err, Msg: fmt.Sprintf("secret path %q contains a symlink", fullPath)}
	case errors.Is(err, safepath.ErrNotRegular):
		return &Error{Kind: ErrInvalid, Ref: ref, Err: err, Msg: fmt.Sprintf("secret %q is not a regular file", string(ref))}
	}
	var ce *safepath.ComponentError
	if errors.As(err, &ce) {
		err = ce.Err
	}
	return &Error{Kind: ErrUnreadable, Ref: ref, Err: err, Msg: fmt.Sprintf("open secret %q: %v", string(ref), err)}
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
