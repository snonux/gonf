package seal

import (
	"errors"
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// This file is the one hardened-open policy every key file this package
// reads goes through: the identity file (LoadIdentities, identities.go,
// task 1b2), the recipients file (LoadRecipientsFile, recipients_file.go,
// task ce2), the signer file (LoadSigner, signer.go, task 6g2) and the
// trusted-signers file (LoadTrustedSigners, signer.go, task 6g2). Each used
// to carry its own copy of the same walk-and-check code; task 6g2 folded
// them into keyFileKind when it added the third and fourth file, so a fix to
// the walk reaches every key file at once instead of one copy at a time.
//
// The kinds differ in exactly three ways, all data rather than code: the
// wording and sentinel errors a refusal carries, the permission bits that
// are refused (modeMask), and what a missing file wraps (missing).

// errKeyFileNotFound is what a missing identity file wraps, keeping
// LoadIdentities' historical "identity file <path>: not found" wording.
var errKeyFileNotFound = errors.New("not found")

// keyFileKind describes one kind of key file: how its refusals are worded
// and which permission bits it refuses.
type keyFileKind struct {
	// label names the file in every error ("identity file").
	label string
	// errSymlink, errNotRegular, errNotOwned and errMode are the sentinel
	// errors a refusal of each class wraps, so callers can errors.Is them.
	errSymlink, errNotRegular, errNotOwned, errMode error
	// modeMask holds the permission bits that must all be clear: 0o077 for
	// a file of private key material (no group or other bit at all, the
	// rule ssh applies to a private key), 0o022 for a file of public keys
	// (only group- and other-WRITE, since reading public keys is not a
	// secrecy problem but writing them lets an attacker inject a key).
	modeMask uint32
	// missing is what a missing file wraps: os.ErrNotExist when the caller
	// needs errors.Is to tell "absent" from "refused", errKeyFileNotFound
	// for the identity file's historical wording.
	missing error
}

// openChecked opens path's final component as a regular file with
// internal/safepath's no-follow walk and requires it to pass k's owner and
// mode policy. No component, including the final file, is ever resolved
// through a symlink, and the policy is checked on the descriptor that is
// then read (Fstat), so a component swapped between a check and the read
// cannot smuggle in a different file. The caller closes the returned file.
func (k keyFileKind) openChecked(path string) (*os.File, error) {
	f, err := k.open(path)
	if err != nil {
		return nil, err
	}
	info, err := safepath.Fstat(int(f.Fd()))
	if err == nil {
		err = k.checkOwnerAndMode(info, path, unix.Geteuid())
	} else {
		err = k.errorf(path, err)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// open walks every directory above path's final component without
// following a symlink and opens that component as a regular file.
func (k keyFileKind) open(path string) (*os.File, error) {
	base, parts := safepath.Split(path)
	if len(parts) == 0 {
		return nil, fmt.Errorf("plan/seal: %s %s: not a file path", k.label, path)
	}
	dirParts, name := parts[:len(parts)-1], parts[len(parts)-1]
	dirFD, err := safepath.Walk{}.Open(base, dirParts)
	if err != nil {
		return nil, k.pathError(path, err)
	}
	defer func() { _ = unix.Close(dirFD) }()
	f, err := safepath.OpenRegularAt(dirFD, name, path)
	if err != nil {
		return nil, k.pathError(path, err)
	}
	return f, nil
}

// pathError classifies a failure to open or walk to path into one of k's
// named classes, discarding the safepath error's own wording (which may
// name a component but never file content, so this is about consistent
// phrasing, not secrecy).
func (k keyFileKind) pathError(path string, err error) error {
	switch {
	case errors.Is(err, safepath.ErrSymlink):
		return k.errorf(path, k.errSymlink)
	case errors.Is(err, safepath.ErrNotRegular):
		return k.errorf(path, k.errNotRegular)
	case errors.Is(err, unix.ENOENT):
		return k.errorf(path, k.missing)
	case errors.Is(err, safepath.ErrInvalidComponent):
		return fmt.Errorf("plan/seal: %s %s: invalid path component", k.label, path)
	default:
		return k.errorf(path, unwrapComponentError(err))
	}
}

// checkOwnerAndMode is the ownership and permission-bit policy itself,
// taking info and the expected effective uid as plain values (rather than
// calling unix.Fstat/unix.Geteuid directly) so a test can exercise "wrong
// owner" with a fabricated safepath.Info instead of needing root to chown a
// real file — the same technique internal/remote/crossbuild_identity_test.go
// uses for a directory's owner.
func (k keyFileKind) checkOwnerAndMode(info safepath.Info, path string, euid int) error {
	if info.UID != uint32(euid) {
		return k.errorf(path, k.errNotOwned)
	}
	if info.Perm()&k.modeMask != 0 {
		return k.errorf(path, k.errMode)
	}
	return nil
}

// errorf wraps err with k's "plan/seal: <label> <path>: " prefix.
func (k keyFileKind) errorf(path string, err error) error {
	return fmt.Errorf("plan/seal: %s %s: %w", k.label, path, err)
}

// unwrapComponentError strips safepath's *ComponentError wrapper (which
// carries the path and name safepath already produced) down to its cause,
// so keyFileKind's own "<label> %s: " prefix is not doubled.
func unwrapComponentError(err error) error {
	var ce *safepath.ComponentError
	if errors.As(err, &ce) {
		return ce.Err
	}
	return err
}
