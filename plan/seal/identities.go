package seal

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"filippo.io/age"
)

// ErrIdentityRefused marks a line LoadIdentities refuses because it names
// an identity type other than age1pq (a classic X25519 identity, or
// anything else), per the package-level "Recipient policy" doc (the same
// policy applies to identities).
var ErrIdentityRefused = errors.New("identity refused: gonf accepts only age1pq (hybrid) identities")

// ErrIdentityMalformed marks a line that looks like an age1pq identity (the
// right prefix) but fails to parse as one.
var ErrIdentityMalformed = errors.New("malformed age1pq identity")

// ErrIdentityNotRegular marks an identity path whose final component is
// not a regular file (a directory, FIFO, device, or socket).
var ErrIdentityNotRegular = errors.New("identity file is not a regular file")

// ErrIdentitySymlink marks an identity path containing a symlink, on the
// final component or anywhere above it.
var ErrIdentitySymlink = errors.New("identity file path contains a symlink")

// ErrIdentityNotOwned marks an identity file not owned by the current
// effective uid.
var ErrIdentityNotOwned = errors.New("identity file is not owned by the current user")

// ErrIdentityMode marks an identity file whose group or other permission
// bits are not all clear (the ssh private-key rule).
var ErrIdentityMode = errors.New("identity file is readable or writable by group or other")

// Identity is a validated age1pq hybrid identity, as returned by
// LoadIdentities and accepted by Open. The zero value is not valid.
type Identity struct {
	inner *age.HybridIdentity
}

// LoadIdentities reads path as an age identity file: one age1pq hybrid
// identity (AGE-SECRET-KEY-PQ-1…) per line, blank lines and "#" comments
// ignored, matching age's own identity-file convention narrowed to hybrid
// keys only (see the package doc's "Recipient policy").
//
// path is opened component by component with internal/safepath's no-follow
// walk (keyFileKind.openChecked, keyfile.go): no component, including the final file, is ever resolved through a
// symlink, so a swapped component cannot smuggle in a different file
// between a check and the read that follows it. The final component must
// additionally be a regular file, owned by the current effective uid, with
// no group or other permission bits — the rule ssh applies to a private
// key. Every refusal names path and the failure class (ErrIdentitySymlink,
// ErrIdentityNotRegular, ErrIdentityNotOwned, ErrIdentityMode, or a parse
// refusal naming a line number and class); none of them include any byte
// of the file's content, since every line here is private key material.
func LoadIdentities(path string) ([]Identity, error) {
	f, err := identityFile.openChecked(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parseIdentityFile(f, path)
}

// identityFile is the identity file's hardened-open policy (keyfile.go):
// private key material, so no group or other permission bit at all.
var identityFile = keyFileKind{
	label:         "identity file",
	errSymlink:    ErrIdentitySymlink,
	errNotRegular: ErrIdentityNotRegular,
	errNotOwned:   ErrIdentityNotOwned,
	errMode:       ErrIdentityMode,
	modeMask:      0o077,
	missing:       errKeyFileNotFound,
}

// parseIdentityFile reads f, already open and positioned at its start, one
// line at a time.
func parseIdentityFile(f *os.File, path string) ([]Identity, error) {
	var out []Identity
	scanner := bufio.NewScanner(f)
	n := 0
	for scanner.Scan() {
		n++
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, err := parseIdentityLine(line)
		if err != nil {
			return nil, fmt.Errorf("plan/seal: identity file %s: line %d: %w", path, n, err)
		}
		out = append(out, Identity{inner: id})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("plan/seal: identity file %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("plan/seal: identity file %s: no age1pq identities found", path)
	}
	return out, nil
}

// parseIdentityLine classifies and validates one non-comment, non-blank
// identity line, without ever including line (private key material) in an
// error.
func parseIdentityLine(line string) (*age.HybridIdentity, error) {
	switch {
	case strings.HasPrefix(line, "AGE-SECRET-KEY-PQ-1"):
		id, err := age.ParseHybridIdentity(line)
		if err != nil {
			return nil, ErrIdentityMalformed
		}
		return id, nil
	case strings.HasPrefix(line, "AGE-SECRET-KEY-1"):
		return nil, fmt.Errorf("%w: classic X25519 identity; generate a hybrid key with age-keygen -pq", ErrIdentityRefused)
	default:
		return nil, fmt.Errorf("%w: unrecognized identity type; generate a hybrid key with age-keygen -pq", ErrIdentityRefused)
	}
}

// toAgeIdentities adapts identities to the age.Identity slice age.Decrypt
// takes.
func toAgeIdentities(identities []Identity) []age.Identity {
	out := make([]age.Identity, len(identities))
	for i, id := range identities {
		out[i] = id.inner
	}
	return out
}
