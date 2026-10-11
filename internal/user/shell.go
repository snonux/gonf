package user

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

// ValidateManagedShell validates the explicit destination passwd shell value.
// It does not require an executable on the controller: the destination chooses
// whether the shell exists or is permitted by its account tools.
func ValidateManagedShell(name, shell string) error {
	switch {
	case shell == "":
		return fmt.Errorf("user %q: managing an existing shell requires a shell", name)
	case !path.IsAbs(shell):
		return fmt.Errorf("user %q: managed shell %q must be absolute", name, shell)
	case path.Clean(shell) != shell || shell == "/":
		return fmt.Errorf("user %q: managed shell %q must be a clean non-root path", name, shell)
	case strings.ContainsRune(shell, ':') || strings.ContainsFunc(shell, unicode.IsControl):
		return fmt.Errorf("user %q: managed shell %q contains a passwd separator or control character", name, shell)
	}
	return nil
}

// passwdShell reads a single complete account record and refuses ambiguous
// output or an account mismatch before allowing any shell-field mutation.
func passwdShell(record, name string, homeField int) (string, error) {
	line := strings.TrimSuffix(record, "\n")
	fields := strings.Split(line, ":")
	if strings.ContainsAny(line, "\n\r\x00") || len(fields) != homeField+2 {
		return "", fmt.Errorf("user %q: malformed passwd entry for shell management", name)
	}
	if fields[0] != name {
		return "", fmt.Errorf("user %q: account lookup returned the entry of account %q; refusing to change the shell", name, fields[0])
	}
	return fields[homeField+1], nil
}

// ensureShellField mutates only the shell field with platform-specific argv.
func (c commands) ensureShellField(record string, want DesiredUser, homeField int, command string, args ...string) error {
	if !want.ManageShell {
		return nil
	}
	current, err := passwdShell(record, want.Name, homeField)
	if err != nil || current == want.Shell {
		return err
	}
	return c.mutateUser(command, args...)
}
