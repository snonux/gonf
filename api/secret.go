package api

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/snonux/gonf/plan"
	"golang.org/x/sys/unix"
)

var errSecretNotRegular = errors.New("secret is not a regular file")

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

func loadSecret(path string) (string, bool, error) {
	fullPath, err := secretPath(path)
	if err != nil {
		return "", false, err
	}
	file, err := openSecret(fullPath)
	if errors.Is(err, unix.ENOENT) {
		return "", false, nil
	}
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return "", false, fmt.Errorf("secret path %q contains a symlink", fullPath)
	}
	if errors.Is(err, errSecretNotRegular) {
		return "", false, fmt.Errorf("secret %q is not a regular file", path)
	}
	if err != nil {
		return "", false, fmt.Errorf("open secret %q: %w", path, err)
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

// openSecret traverses the secrets directory through file descriptors only.
// O_NOFOLLOW applies at every component, so an attacker cannot race a checked
// pathname into a symlink outside the controller-owned secrets tree.
func openSecret(fullPath string) (*os.File, error) {
	rootFD, err := unix.Open("secrets", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fd := rootFD
	parts := strings.Split(strings.TrimPrefix(fullPath, "secrets"+string(filepath.Separator)), string(filepath.Separator))
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, part, flags, 0)
		if err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		if err := unix.Close(fd); err != nil {
			_ = unix.Close(next)
			return nil, err
		}
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, errSecretNotRegular
	}
	return os.NewFile(uintptr(fd), fullPath), nil
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
	return filepath.Join("secrets", clean), nil
}
