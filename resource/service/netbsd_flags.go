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
	updated, err := replaceRcAssignment(content, name, name+"="+shellQuote(flags))
	if err != nil {
		return fmt.Errorf("parse %s: %w", b.rcConf, err)
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
// file at path. A missing file has no assignment. An assignment may span
// several physical lines (a multi-line quoted value, a backslash-newline);
// a quote left open at the end of the file is an error naming its line.
func lastRcAssignment(path, name string) (rcValue, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rcValue{}, false, nil
	}
	if err != nil {
		return rcValue{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	statements, err := splitRcStatements(rcLines(string(data)))
	if err != nil {
		return rcValue{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	var last rcValue
	found := false
	for _, st := range statements {
		if raw, ok := rcAssignment(st.text, name); ok {
			value, parsed := parseShellWord(raw)
			last, found = rcValue{value: value, parsed: parsed}, true
		}
	}
	return last, found, nil
}

// rcAssignment returns the text after "name=" when statement assigns name.
func rcAssignment(statement, name string) (string, bool) {
	return strings.CutPrefix(strings.TrimLeft(statement, " \t"), name+"=")
}

// replaceRcAssignment rewrites content so that its only assignment to name
// is assignment, placed where the first one was (or appended). Every
// physical line of a multi-line assignment is replaced, so no continuation
// line is left behind; content with a quote left open is refused unchanged.
func replaceRcAssignment(content, name, assignment string) (string, error) {
	lines := rcLines(content)
	statements, err := splitRcStatements(lines)
	if err != nil {
		return "", err
	}
	out := make([]string, 0, len(lines)+1)
	placed := false
	for _, st := range statements {
		if _, ok := rcAssignment(st.text, name); !ok {
			out = append(out, lines[st.first:st.end]...)
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

// errUnterminatedQuote reports an rc.conf statement whose quote is still
// open at the end of the file: sh(1) refuses the file, and rewriting part
// of such a statement could only shift the damage elsewhere.
var errUnterminatedQuote = errors.New("unterminated quote")

// rcStatement is one logical line of an rc.conf-style file: the physical
// lines [first, end) that sh(1) reads as one, joined with newlines.
type rcStatement struct {
	first, end int
	text       string
}

// splitRcStatements groups lines into the logical lines sh(1) reads: a
// statement continues onto the next physical line while a quote is open
// or the line ends in an unquoted backslash. Comments are skipped while
// scanning, so an apostrophe in one never opens a quote.
func splitRcStatements(lines []string) ([]rcStatement, error) {
	var (
		statements []rcStatement
		scan       rcScanner
		first      int
	)
	for i, line := range lines {
		if !scan.open() {
			first, scan.wordStart = i, true
		}
		scan.scanLine(line)
		if !scan.open() {
			statements = append(statements, rcStatement{first: first, end: i + 1, text: strings.Join(lines[first:i+1], "\n")})
		}
	}
	if scan.quote != rcUnquoted {
		return nil, fmt.Errorf("line %d: %w", first+1, errUnterminatedQuote)
	}
	if scan.continued { // a backslash-newline right before EOF: sh drops it
		statements = append(statements, rcStatement{first: first, end: len(lines), text: strings.Join(lines[first:], "\n")})
	}
	return statements, nil
}

// rcQuote is the quoting context an rcScanner is in.
type rcQuote int

const (
	rcUnquoted rcQuote = iota
	rcSingleQuoted
	rcDoubleQuoted
)

// rcScanner tracks sh(1) quoting across physical lines, just far enough to
// tell where a statement ends: the open quote, whether the last line ended
// in a backslash-newline, and whether the next character starts a word (so
// a '#' there starts a comment).
type rcScanner struct {
	quote     rcQuote
	continued bool
	wordStart bool
}

// open reports whether the statement scanned so far goes on past the end
// of the last scanned line.
func (s *rcScanner) open() bool { return s.quote != rcUnquoted || s.continued }

// scanLine advances the scanner over one physical line.
func (s *rcScanner) scanLine(line string) {
	s.continued = false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case s.quote == rcSingleQuoted:
			if c == '\'' {
				s.quote = rcUnquoted
			}
		case s.quote == rcDoubleQuoted:
			if c == '"' {
				s.quote = rcUnquoted
			} else if c == '\\' {
				i++ // the escaped character (or newline) cannot close the quote
			}
		case c == '\\' && i+1 == len(line):
			s.continued = true // backslash-newline: the statement goes on
			return
		case c == '\\':
			i++ // the escaped character is literal
			s.wordStart = false
		case c == '#' && s.wordStart:
			return // a comment runs to the end of the line
		default:
			s.scanUnquoted(c)
		}
	}
	if s.quote == rcUnquoted {
		s.wordStart = true // an unquoted newline separates words
	}
}

// scanUnquoted handles an unquoted character other than a backslash or a
// comment-starting '#'.
func (s *rcScanner) scanUnquoted(c byte) {
	switch c {
	case '\'':
		s.quote, s.wordStart = rcSingleQuoted, false
	case '"':
		s.quote, s.wordStart = rcDoubleQuoted, false
	case ' ', '\t', ';', '&', '|', '(', ')', '<', '>':
		s.wordStart = true
	default:
		s.wordStart = false
	}
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
