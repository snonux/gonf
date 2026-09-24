package service

// NetBSD WithFlags support: NetBSD has no sysrc or rcctl, so the backend
// reads and edits the NAME_flags assignment in /etc/rc.conf itself.
//
// rc.subr's load_rc_config sources /etc/rc.conf (which itself sources
// /etc/defaults/rc.conf first) and then /etc/rc.conf.d/NAME, so the
// effective value is the last assignment in the highest of those three.
// flagsMatch reads them in that precedence; setFlags writes /etc/rc.conf
// only. An assignment in /etc/rc.conf.d/NAME that differs is refused
// instead of shadowed, since writing rc.conf could never take effect.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Production rc.conf paths of the NetBSD flags support. Tests build a
// netbsdBackend with temporary ones instead of touching /etc.
const (
	netbsdRcConf         = "/etc/rc.conf"
	netbsdRcConfDefaults = "/etc/defaults/rc.conf"
)

var _ flagger = netbsdBackend{}

// flagsMatch reports whether the effective NAME_flags value equals want.
// An assignment it cannot parse (an expansion, a command substitution, a
// second statement on the line) never matches, so setFlags rewrites it.
func (b netbsdBackend) flagsMatch(u unit, want string) (bool, error) {
	name, err := flagsVar(u.name)
	if err != nil {
		return false, err
	}
	override := filepath.Join(b.rcConfD, u.name)
	value, found, err := lastRcAssignment(override, name)
	if err != nil {
		return false, err
	}
	if found {
		if value.parsed && value.value == want {
			return true, nil
		}
		return false, fmt.Errorf("service[%s]: %s is set in %s, which overrides %s; remove it there to let WithFlags manage it",
			u.name, name, override, b.rcConf)
	}
	for _, path := range []string{b.rcConf, b.rcConfDefaults} {
		value, found, err := lastRcAssignment(path, name)
		if err != nil {
			return false, err
		}
		if found {
			return value.parsed && value.value == want, nil
		}
	}
	return want == "", nil // unset everywhere: the daemon gets no flags
}

// setFlags replaces every NAME_flags assignment in rc.conf with one
// single-quoted NAME_flags='FLAGS' line, at the first assignment's place,
// or appends it when there is none. The file is replaced atomically and
// keeps its mode.
func (b netbsdBackend) setFlags(u unit, flags string) error {
	name, err := flagsVar(u.name)
	if err != nil {
		return err
	}
	content, mode, err := readRcConf(b.rcConf)
	if err != nil {
		return err
	}
	updated := replaceRcAssignment(content, name, name+"="+shellQuote(flags))
	return writeFileAtomic(b.rcConf, []byte(updated), mode)
}

func (b netbsdBackend) describeFlags(u unit, flags string) (would, did string) {
	desc := fmt.Sprintf("set %s_flags=%s in %s", u.name, shellQuote(flags), b.rcConf)
	return desc, desc
}

// rcValue is one parsed rc.conf assignment value; parsed is false when the
// shell word used syntax this parser does not evaluate.
type rcValue struct {
	value  string
	parsed bool
}

// lastRcAssignment returns the last assignment to name in the rc.conf-style
// file at path. A missing file has no assignment.
func lastRcAssignment(path, name string) (rcValue, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rcValue{}, false, nil
	}
	if err != nil {
		return rcValue{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var last rcValue
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if raw, ok := rcAssignment(line, name); ok {
			value, parsed := parseShellWord(raw)
			last, found = rcValue{value: value, parsed: parsed}, true
		}
	}
	return last, found, nil
}

// rcAssignment returns the text after "name=" when line assigns name.
func rcAssignment(line, name string) (string, bool) {
	return strings.CutPrefix(strings.TrimLeft(line, " \t"), name+"=")
}

// replaceRcAssignment rewrites content so that its only assignment to name
// is assignment, placed where the first one was (or appended).
func replaceRcAssignment(content, name, assignment string) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	out := make([]string, 0, len(lines)+1)
	placed := false
	for _, line := range lines {
		if _, ok := rcAssignment(line, name); !ok {
			out = append(out, line)
			continue
		}
		if !placed {
			out = append(out, assignment)
			placed = true
		}
	}
	if !placed {
		out = append(out, assignment)
	}
	return strings.Join(out, "\n") + "\n"
}

// readRcConf returns rc.conf's content and mode; a missing file is empty
// with mode 0644.
func readRcConf(path string) (string, fs.FileMode, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0o644, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), info.Mode().Perm(), nil
}

// writeFileAtomic writes data to a temporary file beside path and renames
// it over path, so rc(8) never reads a half-written rc.conf.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".gonf-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}

// shellQuote single-quotes s for sh(1), so rc.conf assigns it literally.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// parseShellWord evaluates the value of an rc.conf assignment: one shell
// word of unquoted, single-quoted and double-quoted parts, optionally
// followed by blanks and a comment. It reports false for anything it does
// not evaluate: parameter expansion, command substitution, or more text
// after the word (a second statement).
func parseShellWord(raw string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; c {
		case ' ', '\t':
			rest := strings.TrimLeft(raw[i:], " \t")
			return b.String(), rest == "" || rest[0] == '#'
		case '\'':
			end := strings.IndexByte(raw[i+1:], '\'')
			if end < 0 {
				return "", false
			}
			b.WriteString(raw[i+1 : i+1+end])
			i += end + 1
		case '"':
			n, ok := parseDoubleQuoted(raw[i+1:], &b)
			if !ok {
				return "", false
			}
			i += n + 1
		case '\\':
			if i+1 >= len(raw) {
				return "", false
			}
			i++
			b.WriteByte(raw[i])
		case '$', '`', ';', '&', '|', '<', '>', '(', ')':
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// parseDoubleQuoted appends the double-quoted text at the start of s (after
// the opening quote) to b and returns the index of the closing quote in s.
// Inside double quotes a backslash escapes only $ ` " \ ; an expansion is
// not evaluated (false).
func parseDoubleQuoted(s string, b *strings.Builder) (int, bool) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			return i, true
		case '$', '`':
			return 0, false
		case '\\':
			if i+1 < len(s) && strings.IndexByte("$`\"\\", s[i+1]) >= 0 {
				i++
				b.WriteByte(s[i])
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return 0, false
}
