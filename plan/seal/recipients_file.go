package seal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// This file is task ce2 (docs/design/plan-encryption.md "Keys"): the operator
// recipients file used to be read with a plain os.ReadFile by
// internal/cli/plan_seal.go, with no ownership, permission or symlink
// check at all — unlike the identity file (LoadIdentities, identities.go),
// which already gets internal/safepath's hardened no-follow walk. Since
// the recipients file decides who can decrypt every plan this account ever
// seals, that asymmetry was a real, probed vulnerability: a symlinked or
// world-writable recipients file let an attacker silently become a
// permanent recipient (see the task's annotation for the reproduction).
// LoadRecipientsFile closes it by giving the recipients file the same
// walk LoadIdentities uses (keyFileKind, keyfile.go, shared since task
// 6g2), adjusted for the one place their trust models differ
// (ErrRecipientsFileWritable's doc comment).

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
	f, err := recipientsFile.openChecked(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readRecipientsFileLines(f, path)
}

// recipientsFile is the recipients file's hardened-open policy
// (keyfile.go): public keys, so only group- and other-write are refused
// (see ErrRecipientsFileWritable), and a missing file wraps os.ErrNotExist
// so the caller can tell "absent" from "refused" (see LoadRecipientsFile).
var recipientsFile = keyFileKind{
	label:         "recipients file",
	errSymlink:    ErrRecipientsFileSymlink,
	errNotRegular: ErrRecipientsFileNotRegular,
	errNotOwned:   ErrRecipientsFileNotOwned,
	errMode:       ErrRecipientsFileWritable,
	modeMask:      0o022,
	missing:       os.ErrNotExist,
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
