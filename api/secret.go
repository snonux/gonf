package api

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/internal/safepath"
	"github.com/snonux/gonf/plan"
	"golang.org/x/sys/unix"
)

// secretsDir is the controller-local directory secrets are read from,
// relative to the working directory (the recipe checkout).
const secretsDir = "secrets"

// MustSecret reads the non-empty controller-local secret at secrets/path.
// It preserves every byte exactly, including leading/trailing whitespace and
// newlines. A missing, unreadable, empty, or unsafe path fails plan recording
// before a local apply or remote push can begin. Secret contents are never
// included in the returned error.
//
// A leading slash is accepted for compatibility with the Rex convention:
// MustSecret("/var/nsd/key") reads secrets/var/nsd/key, not /var/nsd/key on
// the controller. Paths may not escape the secrets directory.
func MustSecret(path string) string {
	secret, ok, err := loadSecret(path)
	if err != nil {
		stashSecretError(err)
		return ""
	}
	if !ok {
		stashSecretError(fmt.Errorf("secret %q is missing", path))
		return ""
	}
	return secret
}

// OptionalSecret reads the non-empty controller-local secret at secrets/path.
// It preserves every byte exactly. It returns ("", false) only when the file
// is missing, so callers can omit a host-specific plan fragment. An unreadable,
// empty, or unsafe path fails plan recording before a local apply or remote
// push can begin. Secret contents are never included in the returned error.
//
// A leading slash is accepted for compatibility with the Rex convention; the
// path remains rooted below the recipe's secrets directory.
func OptionalSecret(path string) (string, bool) {
	secret, ok, err := loadSecret(path)
	if err != nil {
		stashSecretError(err)
		return "", false
	}
	return secret, ok
}

func stashSecretError(err error) {
	// Secrets are controller inputs, so their failures are record-time runtime
	// errors—not registration-time DSL misuse. Stashing lets RecordPlan/Run
	// return normally through their deferred cleanup and keeps PushTo from
	// opening an SSH connection.
	if plan.Recording() {
		stashBodyError(err)
		return
	}
	panic(err)
}

// loadSecret reads secrets/path. A secret that is missing anywhere on its
// path (secrets/ itself included) is ("", false, nil); every other failure is
// an error that names the secret but never contains its value.
func loadSecret(path string) (string, bool, error) {
	fullPath, err := secretPath(path)
	if err != nil {
		return "", false, err
	}
	file, err := openSecret(fullPath)
	if errors.Is(err, unix.ENOENT) {
		return "", false, nil
	}
	if err != nil {
		return "", false, secretOpenError(path, fullPath, err)
	}
	defer func() { _ = file.Close() }()
	data, err := readSecretFile(file)
	if err != nil {
		return "", false, fmt.Errorf("read secret %q: %w", path, err)
	}
	if len(data) == 0 {
		return "", false, fmt.Errorf("secret %q is empty", path)
	}
	return string(data), true, nil
}

// secretOpenError words a failure of openSecret. A symlink anywhere on the
// path is reported as such; so is ENOTDIR, a component that is a regular file
// where a directory is needed, which the walk has always reported with the
// symlink wording (and ELOOP, which Linux gives for a symlink opened
// O_NOFOLLOW, is kept for safety although safepath already diagnoses it as
// safepath.ErrSymlink). Other errors keep the bare cause after the secret's
// name, without the path the walk adds.
func secretOpenError(path, fullPath string, err error) error {
	switch {
	case errors.Is(err, safepath.ErrSymlink), errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		return fmt.Errorf("secret path %q contains a symlink", fullPath)
	case errors.Is(err, safepath.ErrNotRegular):
		return fmt.Errorf("secret %q is not a regular file", path)
	}
	var ce *safepath.ComponentError
	if errors.As(err, &ce) {
		err = ce.Err
	}
	return fmt.Errorf("open secret %q: %w", path, err)
}

// openSecret opens fullPath (secrets/<clean path>, see secretPath) with the
// shared descriptor walk of internal/safepath: secrets/ is opened relative to
// the working directory without following it, every directory below it
// relative to its parent's descriptor with O_NOFOLLOW, and the secret itself
// O_NOFOLLOW|O_NONBLOCK and only when it is a regular file. An attacker can
// therefore not race a checked pathname into a symlink outside the
// controller-owned secrets tree. It also cannot climb out of it: the walk
// would follow a ".." component, but secretPath has already refused any path
// that keeps one after cleaning. Nothing is created, and ownership and modes
// are not checked: the secrets tree is the operator's own checkout.
func openSecret(fullPath string) (*os.File, error) {
	rel := strings.TrimPrefix(fullPath, secretsDir+string(filepath.Separator))
	parts := strings.Split(rel, string(filepath.Separator))
	dirs, name := parts[:len(parts)-1], parts[len(parts)-1]
	dirFD, err := safepath.Walk{}.Open(secretsDir, dirs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	return safepath.OpenRegularAt(dirFD, name, fullPath)
}

func readSecretFile(file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func secretPath(path string) (string, error) {
	trimmed := strings.TrimLeft(path, "/\\")
	if trimmed == "" {
		return "", fmt.Errorf("secret path must not be empty")
	}
	clean := filepath.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid secret path %q", path)
	}
	return filepath.Join(secretsDir, clean), nil
}
