package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// SecureDir creates dir when needed and makes it owner-only. Every component
// is opened relative to a held descriptor with O_NOFOLLOW.
func SecureDir(dir string) error {
	fd, err := openSecureDir(dir)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return unix.Fchmod(fd, 0o700)
}

// WritePrivateFile atomically replaces name below dir with an owner-only file.
// The data is written through a fresh O_EXCL descriptor before rename, so a
// permissive pre-existing file or symlink never receives secret plan material.
func WritePrivateFile(dir, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." {
		return fmt.Errorf("plan: invalid private file name %q", name)
	}
	dirFD, err := openSecureDir(dir)
	if err != nil {
		return fmt.Errorf("plan: open private directory: %w", err)
	}
	defer func() { _ = unix.Close(dirFD) }()
	tempName := "." + name + ".tmp-" + strconv.Itoa(os.Getpid())
	fileFD, err := unix.Openat(dirFD, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("plan: create private file: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), tempName)
	defer func() { _ = unix.Unlinkat(dirFD, tempName, 0) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("plan: write private file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("plan: close private file: %w", err)
	}
	if err := unix.Renameat(dirFD, tempName, dirFD, name); err != nil {
		return fmt.Errorf("plan: replace private file: %w", err)
	}
	return nil
}

func openSecureDir(dir string) (int, error) {
	clean := filepath.Clean(dir)
	base := "."
	parts := strings.Split(clean, string(filepath.Separator))
	if filepath.IsAbs(clean) {
		base = string(filepath.Separator)
		parts = strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	}
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			err = unix.Mkdirat(fd, part, 0o700)
			if err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		if err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
		if err := unix.Close(fd); err != nil {
			_ = unix.Close(next)
			return -1, err
		}
		fd = next
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}
