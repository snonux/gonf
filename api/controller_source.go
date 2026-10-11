package api

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
)

// OptionalControllerDir reports whether path is a readable directory on the
// controller. Missing directories return false without error. Every other
// failure (including a wrong file type) reports a declaration error naming
// purpose and path, and returns false. Use it to skip the declarations that
// depend on an optional checkout. Unlike WhenPathExists, it always probes
// the controller, even while recording a plan; no destination guard is emitted.
// Symlinks to directories are accepted. path and purpose must be non-empty.
func OptionalControllerDir(path, purpose string) bool {
	if err := controllerSourceArgs(path, purpose); err != nil {
		declerr.Report(err)
		return false
	}
	source := localPath(Expand(path))
	err := readableControllerDir(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		declerr.Reportf("%s: cannot read optional controller directory %s: %w", purpose, source, err)
		return false
	}
	return true
}

// RequireControllerFile returns the expanded controller path when it names a
// readable regular file, following symlinks. On failure it reports a declaration
// error naming purpose, path and hint, and returns ""; skip declarations that
// depend on the file when the result is empty. It always runs on the controller,
// including during plan recording, and emits no destination guard. path,
// purpose and hint must be non-empty. This checks the file now; later source
// packaging still reads it and may fail if it has changed or disappeared.
func RequireControllerFile(path, purpose, hint string) string {
	if err := controllerSourceArgs(path, purpose); err != nil {
		declerr.Report(err)
		return ""
	}
	if strings.TrimSpace(hint) == "" {
		declerr.Reportf("%s: required controller file %s needs a non-empty hint", purpose, path)
		return ""
	}
	source := localPath(Expand(path))
	if err := readableControllerFile(source); err != nil {
		declerr.Reportf("%s needs controller file %s: %w; %s", purpose, source, err, hint)
		return ""
	}
	return source
}

func controllerSourceArgs(path, purpose string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(purpose) == "" {
		return fmt.Errorf("controller source check: path and purpose must be non-empty")
	}
	return nil
}

// readableControllerFile returns errors below the DSL. Stat rejects special
// files before Open (a FIFO must not be opened and waited on as a source).
func readableControllerFile(path string) (err error) {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if _, err = io.Copy(io.Discard, f); err != nil {
		return err
	}
	return nil
}

func readableControllerDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	_, err = os.ReadDir(path)
	return err
}
