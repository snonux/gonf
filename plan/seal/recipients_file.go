package seal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/snonux/gonf/internal/safepath"
	"golang.org/x/sys/unix"
)

// This file is task ce2 (docs/plan-encryption.md "Keys"): the operator
// recipients file used to be read with a plain os.ReadFile by
// internal/cli/plan_seal.go, with no ownership, permission or symlink
// check at all — unlike the identity file (LoadIdentities, identities.go),
// which already gets internal/safepath's hardened no-follow walk. Since
// the recipients file decides who can decrypt every plan this account ever
// seals, that asymmetry was a real, probed vulnerability: a symlinked or
// world-writable recipients file let an attacker silently become a
// permanent recipient (see the task's annotation for the reproduction).
// LoadRecipientsFile closes it by giving the recipients file the same
// walk LoadIdentities uses, adjusted for the one place their trust models
// differ (ErrRecipientsFileWritable's doc comment).

// ErrRecipientsFileNotRegular marks a recipients file path whose final
// component is not a regular file (a directory, FIFO, device, or socket).
var ErrRecipientsFileNotRegular = errors.New("recipients file is not a regular file")

// ErrRecipientsFileSymlink marks a recipients file path containing a
// symlink, on the final component or anywhere above it.
var ErrRecipientsFileSymlink = errors.New("recipients file path contains a symlink")

// ErrRecipientsFileNotOwned marks a recipients file not owned by the
// current effective uid.
var ErrRecipientsFileNotOwned = errors.New("recipients file is not owned by the current user")

// ErrRecipientsFileWritable marks a recipients file writable by group or
// other. Unlike an identity file (LoadIdentities, ErrIdentityMode, which
// refuses ANY group or other permission bit because reading it leaks
// private key material), a recipients file holds only PUBLIC keys:
// another local account reading it is not a secrecy problem (docs/
// plan-encryption.md "Threat model", T1), but writing it is exactly the
// attack this rule closes — whoever can write this file, or plant a
// symlink at its path, silently becomes a permanent recipient of every
// plan this account seals from then on. So only the group- and
// other-write bits (0o022) are checked here, never the read bits — a
// deliberately narrower rule than LoadIdentities' 0o077, justified by the
// file's own content being public rather than secret.
var ErrRecipientsFileWritable = errors.New("recipients file is writable by group or other")

// LoadRecipientsFile reads path as an operator recipients file: raw
// lines, unparsed ("#" comments and blank lines left in, exactly as
// ParseRecipients expects them) — the caller unions them with other
// recipient sources (e.g. -recipient flags) before calling ParseRecipients
// itself.
//
// path is opened component by component with internal/safepath's
// no-follow walk, exactly as LoadIdentities opens an identity file: no
// component, including the final file, is ever resolved through a
// symlink, so a swapped component or a symlinked file cannot smuggle in a
// different file between a check and the read that follows it. The final
// component must additionally be a regular file, owned by the current
// effective uid, with no group- or other-write permission bit (see
// ErrRecipientsFileWritable's doc comment for why this check is narrower
// than LoadIdentities' own).
//
// A missing file is reported as an error satisfying errors.Is(err,
// os.ErrNotExist), never swallowed here: whether that is acceptable — an
// operator who seals only with -recipient flags need never create a
// DEFAULT recipients file, but one explicitly named with -recipients-file
// should not go silently missing — is the caller's policy, not this
// package's.
//
// No refusal here ever includes path's content: every line is a public
// key, so omitting it is about matching LoadIdentities' habit for
// consistency, not secrecy.
func LoadRecipientsFile(path string) ([]string, error) {
	f, err := openRecipientsFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := checkRecipientsFileOwnerAndMode(f, path); err != nil {
		return nil, err
	}
	return readRecipientsFileLines(f, path)
}

// openRecipientsFile opens path's final component as a regular file,
// walking every directory above it with safepath's no-follow Walk — the
// same technique openIdentityFile (identities.go) uses.
func openRecipientsFile(path string) (*os.File, error) {
	base, parts := safepath.Split(path)
	if len(parts) == 0 {
		return nil, fmt.Errorf("plan/seal: recipients file %s: not a file path", path)
	}
	dirParts, name := parts[:len(parts)-1], parts[len(parts)-1]
	dirFD, err := safepath.Walk{}.Open(base, dirParts)
	if err != nil {
		return nil, recipientsFilePathError(path, err)
	}
	defer func() { _ = unix.Close(dirFD) }()
	f, err := safepath.OpenRegularAt(dirFD, name, path)
	if err != nil {
		return nil, recipientsFilePathError(path, err)
	}
	return f, nil
}

// recipientsFilePathError classifies a failure to open or walk to path,
// mirroring identityPathError (identities.go) but wrapping a missing file
// with %w os.ErrNotExist instead of a bare "not found" string, so a
// caller can distinguish a genuinely missing file from every other
// refusal with errors.Is — see LoadRecipientsFile's doc comment.
func recipientsFilePathError(path string, err error) error {
	switch {
	case errors.Is(err, safepath.ErrSymlink):
		return fmt.Errorf("plan/seal: recipients file %s: %w", path, ErrRecipientsFileSymlink)
	case errors.Is(err, safepath.ErrNotRegular):
		return fmt.Errorf("plan/seal: recipients file %s: %w", path, ErrRecipientsFileNotRegular)
	case errors.Is(err, unix.ENOENT):
		return fmt.Errorf("plan/seal: recipients file %s: %w", path, os.ErrNotExist)
	case errors.Is(err, safepath.ErrInvalidComponent):
		return fmt.Errorf("plan/seal: recipients file %s: invalid path component", path)
	default:
		return fmt.Errorf("plan/seal: recipients file %s: %w", path, unwrapComponentError(err))
	}
}

// checkRecipientsFileOwnerAndMode requires f (already known regular and
// reached without following a symlink) to be owned by the current
// effective uid with no group- or other-write permission bit.
func checkRecipientsFileOwnerAndMode(f *os.File, path string) error {
	info, err := safepath.Fstat(int(f.Fd()))
	if err != nil {
		return fmt.Errorf("plan/seal: recipients file %s: %w", path, err)
	}
	return checkRecipientsOwnerAndMode(info, path, unix.Geteuid())
}

// checkRecipientsOwnerAndMode is the ownership and permission-bit policy
// itself, taking info and the expected effective uid as plain values
// (rather than calling unix.Fstat/unix.Geteuid directly) so a test can
// exercise "wrong owner" or "group-writable" with a fabricated
// safepath.Info instead of needing root — the same technique
// identities.go's checkOwnerAndMode uses.
func checkRecipientsOwnerAndMode(info safepath.Info, path string, euid int) error {
	if info.UID != uint32(euid) {
		return fmt.Errorf("plan/seal: recipients file %s: %w", path, ErrRecipientsFileNotOwned)
	}
	if info.Perm()&0o022 != 0 {
		return fmt.Errorf("plan/seal: recipients file %s: %w", path, ErrRecipientsFileWritable)
	}
	return nil
}

// readRecipientsFileLines reads f, already open and positioned at its
// start, as raw lines for the caller's own ParseRecipients call — this
// function does no recipient-syntax validation itself, only the hardened
// read.
func readRecipientsFileLines(f *os.File, path string) ([]string, error) {
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("plan/seal: recipients file %s: %w", path, err)
	}
	return strings.Split(string(data), "\n"), nil
}
