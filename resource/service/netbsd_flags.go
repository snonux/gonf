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
// matches, so setFlags rewrites it when that is safe. Neither does an
// unset one in rc.conf or the defaults that source other files (or eval),
// which may set it unseen: the assignment setFlags appends settles it.
func (b netbsdBackend) flagsMatch(u unit, want string) (bool, error) {
	name, err := flagsVar(u.name)
	if err != nil {
		return false, err
	}
	override := filepath.Join(b.rcConfD, u.name)
	value, err := readRcAssignment(override, name)
	switch {
	case err != nil:
		return false, err
	case value.found && value.parsed && value.value == want:
		return true, nil
	case value.found:
		return false, fmt.Errorf("service[%s]: %s is set in %s, which overrides %s; remove it there to let WithFlags manage it",
			u.name, name, override, b.rcConf)
	case value.includesOther:
		return false, fmt.Errorf("service[%s]: %s sources other files or uses eval, which may set %s over %s; WithFlags cannot tell its value",
			u.name, override, name, b.rcConf)
	}
	for _, path := range []string{b.rcConf, b.rcConfDefaults} {
		value, err := readRcAssignment(path, name)
		switch {
		case err != nil:
			return false, err
		case value.found:
			return value.parsed && value.value == want, nil
		case value.includesOther:
			return false, nil
		}
	}
	return want == "", nil // unset everywhere: the daemon gets no flags
}

// setFlags sets NAME_flags='FLAGS' in rc.conf: the first assignment's word
// is replaced in place (keeping export, other statements and comments on
// its line), later duplicates alone on their line are dropped, or the line
// is appended when there is none. It refuses, leaving rc.conf alone, when
// the result would not read back as exactly FLAGS (see
// replaceRcAssignment). The file is replaced atomically and keeps its mode.
func (b netbsdBackend) setFlags(u unit, flags string) error {
	name, err := flagsVar(u.name)
	if err != nil {
		return err
	}
	content, mode, err := readRcConf(b.rcConf)
	if err != nil {
		return err
	}
	updated, err := replaceRcAssignment(content, name, flags)
	if err != nil {
		return fmt.Errorf("rewrite %s: %w", b.rcConf, err)
	}
	return writeFileAtomic(b.rcConf, []byte(updated), mode)
}

func (b netbsdBackend) describeFlags(u unit, flags string) (would, did string) {
	desc := fmt.Sprintf("set %s_flags=%s in %s", u.name, shellQuote(flags), b.rcConf)
	return desc, desc
}

// rcValue is what one rc.conf-style file says about a variable: whether
// it assigns it (found), the last value (parsed is false when the shell
// word used syntax this parser does not evaluate), and whether the file
// sources other files or uses eval (includesOther), which may set it
// unseen.
type rcValue struct {
	value         string
	parsed, found bool
	includesOther bool
}

// readRcAssignment reads the last assignment to name in the rc.conf-style
// file at path. A missing file has no assignment. The file is read as
// sh(1) reads it (see rcconf.go): an assignment may span several physical
// lines or follow others on its line, and text it cannot bound, or an
// assignment whose effect it cannot tell, is an error naming the line.
func readRcAssignment(path, name string) (rcValue, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rcValue{}, nil
	}
	if err != nil {
		return rcValue{}, fmt.Errorf("read %s: %w", path, err)
	}
	scan, err := scanRcAssignments(string(data), name)
	if err != nil {
		return rcValue{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(scan.found) == 0 {
		return rcValue{includesOther: scan.includesOther}, nil
	}
	value, parsed := parseShellWord(scan.found[len(scan.found)-1].value)
	return rcValue{value: value, parsed: parsed, found: true, includesOther: scan.includesOther}, nil
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
