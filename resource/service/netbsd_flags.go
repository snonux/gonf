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
//
// The files are read as sh(1) reads them (rcconf_lex.go, rcconf.go), and
// the edit fails closed: whatever the parser does not fully understand is
// an error naming the file and line, never deleted or rewritten.

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
// A value it cannot evaluate (an expansion, a command substitution) never
// matches, so setFlags rewrites it when that is safe.
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
// or appends it when there is none. It refuses, leaving rc.conf alone, when
// replacing an assignment's lines could drop or orphan other shell text
// (see replaceRcAssignment). The file is replaced atomically and keeps its
// mode.
func (b netbsdBackend) setFlags(u unit, flags string) error {
	name, err := flagsVar(u.name)
	if err != nil {
		return err
	}
	content, mode, err := readRcConf(b.rcConf)
	if err != nil {
		return err
	}
	updated, err := replaceRcAssignment(content, name, name+"="+shellQuote(flags))
	if err != nil {
		return fmt.Errorf("rewrite %s: %w", b.rcConf, err)
	}
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
// file at path. A missing file has no assignment. The file is read as
// sh(1) reads it (see rcconf.go): an assignment may span several physical
// lines or follow others on its line, and text it cannot bound, or an
// assignment whose effect it cannot tell, is an error naming the line.
func lastRcAssignment(path, name string) (rcValue, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rcValue{}, false, nil
	}
	if err != nil {
		return rcValue{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	found, err := findRcAssignments(string(data), name)
	if err != nil {
		return rcValue{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(found) == 0 {
		return rcValue{}, false, nil
	}
	value, parsed := parseShellWord(found[len(found)-1].value)
	return rcValue{value: value, parsed: parsed}, true, nil
}

// replaceRcAssignment rewrites content so that its only assignment to name
// is assignment, placed where the first one was (or appended). It fails
// closed: every other line is kept byte for byte, and it refuses (leaving
// content to the caller unchanged) when an assignment shares its logical
// line with other shell text, or spans several physical lines with a value
// it cannot evaluate, since replacing its lines could then drop or orphan
// text it does not understand.
func replaceRcAssignment(content, name, assignment string) (string, error) {
	found, err := findRcAssignments(content, name)
	if err != nil {
		return "", err
	}
	replaced := make(map[int]int, len(found)) // first line -> end line
	for _, a := range found {
		if !a.alone {
			return "", fmt.Errorf("line %d: %s shares its line with other shell text, which a rewrite would drop: %w",
				a.line+1, name, errRcUnmanaged)
		}
		if _, parsed := parseShellWord(a.value); !parsed && a.end-a.first > 1 {
			return "", fmt.Errorf("line %d: %s spans lines %d-%d with a value WithFlags cannot evaluate: %w",
				a.line+1, name, a.first+1, a.end, errRcUnmanaged)
		}
		replaced[a.first] = a.end
	}
	lines := rcLines(content)
	out := make([]string, 0, len(lines)+1)
	placed := false
	for i := 0; i < len(lines); i++ {
		end, ok := replaced[i]
		if !ok {
			out = append(out, lines[i])
			continue
		}
		if !placed {
			out = append(out, assignment)
			placed = true
		}
		i = end - 1
	}
	if !placed {
		out = append(out, assignment)
	}
	return strings.Join(out, "\n") + "\n", nil
}

// rcLines splits content into its physical lines; a final newline does not
// start another (empty) line.
func rcLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
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
// after the word (a second statement). raw may span several lines: a
// quoted newline is kept, and a backslash-newline outside single quotes is
// a line continuation that sh(1) removes.
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
			if raw[i] != '\n' {
				b.WriteByte(raw[i])
			}
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
// Inside double quotes a backslash escapes only $ ` " \ and removes a
// following newline; an expansion is not evaluated (false).
func parseDoubleQuoted(s string, b *strings.Builder) (int, bool) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			return i, true
		case '$', '`':
			return 0, false
		case '\\':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++ // line continuation
				continue
			}
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
