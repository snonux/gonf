package service

// A small sh(1) lexer for rc.conf-style files. It knows just enough of the
// POSIX shell grammar to bound every word, operator and logical line —
// quotes, $'...', nested $(...), ${...} and backquotes, backslash-newline
// continuations, comments and here-document bodies — so the NetBSD flags
// support never mistakes quoted text for an assignment or cuts a statement
// in half. It fails closed: text it cannot bound with certainty is an
// error (errRcSyntax) naming its line, never a guess.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// errRcSyntax marks rc.conf text the lexer cannot bound with certainty: an
// unterminated quote, substitution or here-document, or a construct it does
// not model.
var errRcSyntax = errors.New("unterminated or unsupported shell syntax")

type rcTokenKind int

const (
	rcWord rcTokenKind = iota
	rcOperator
	rcNewline
)

// rcToken is one lexed token; [start, end) are byte offsets in the source.
// A newline token's end lies past the here-document bodies it starts.
type rcToken struct {
	kind       rcTokenKind
	text       string
	start, end int
}

// rcOperators lists the sh(1) operators, longest first so a prefix match
// picks the longest one.
var rcOperators = []string{"<<-", ";;", "&&", "||", "<<", ">>", "<&", ">&", "<>", ">|", ";", "&", "|", "<", ">", "(", ")"}

func isRcOperatorChar(c byte) bool { return strings.IndexByte(";&|<>()", c) >= 0 }

func isRcBlank(c byte) bool { return c == ' ' || c == '\t' }

// rcHeredoc is a here-document whose body starts after the next newline.
type rcHeredoc struct {
	delim     string
	stripTabs bool
	start     int
}

type rcLexer struct {
	src      string
	pos      int
	tokens   []rcToken
	heredocs []rcHeredoc
}

// lexRc splits src into words, operators and newlines. Comments,
// backslash-newlines between words and here-document bodies produce no
// token.
func lexRc(src string) ([]rcToken, error) {
	l := &rcLexer{src: src}
	for l.pos < len(l.src) {
		if err := l.next(); err != nil {
			return nil, err
		}
	}
	if len(l.heredocs) > 0 {
		h := l.heredocs[0]
		return nil, l.fail(h.start, fmt.Sprintf("here-document without its closing %q line", h.delim))
	}
	return l.tokens, nil
}

func (l *rcLexer) fail(off int, what string) error {
	return fmt.Errorf("line %d: %s: %w", strings.Count(l.src[:off], "\n")+1, what, errRcSyntax)
}

func (l *rcLexer) emit(kind rcTokenKind, start int) {
	l.tokens = append(l.tokens, rcToken{kind: kind, text: l.src[start:l.pos], start: start, end: l.pos})
}

func (l *rcLexer) next() error {
	switch c := l.src[l.pos]; {
	case isRcBlank(c):
		l.pos++
	case strings.HasPrefix(l.src[l.pos:], "\\\n"):
		l.pos += 2
	case c == '#':
		l.skipComment()
	case c == '\n':
		l.pos++
		l.emit(rcNewline, l.pos-1)
		return l.skipHeredocBodies()
	case isRcOperatorChar(c):
		return l.operator()
	default:
		return l.word()
	}
	return nil
}

func (l *rcLexer) skipComment() {
	if end := strings.IndexByte(l.src[l.pos:], '\n'); end >= 0 {
		l.pos += end
	} else {
		l.pos = len(l.src)
	}
}

func (l *rcLexer) operator() error {
	start := l.pos
	for _, op := range rcOperators {
		if !strings.HasPrefix(l.src[l.pos:], op) {
			continue
		}
		l.pos += len(op)
		l.emit(rcOperator, start)
		if op == "<<" || op == "<<-" {
			return l.heredocDelimiter(start, op == "<<-")
		}
		return nil
	}
	return l.fail(start, "unknown operator") // unreachable: every operator char starts one
}

// heredocDelimiter lexes the delimiter word after << or <<- and queues its
// body, which starts after the next newline.
func (l *rcLexer) heredocDelimiter(start int, stripTabs bool) error {
	for l.pos < len(l.src) && isRcBlank(l.src[l.pos]) {
		l.pos++
	}
	if l.pos == len(l.src) || l.src[l.pos] == '\n' || isRcOperatorChar(l.src[l.pos]) {
		return l.fail(start, "here-document without a delimiter")
	}
	if err := l.word(); err != nil {
		return err
	}
	delim := strings.NewReplacer(`\`, "", `'`, "", `"`, "").Replace(l.tokens[len(l.tokens)-1].text)
	l.heredocs = append(l.heredocs, rcHeredoc{delim: delim, stripTabs: stripTabs, start: start})
	return nil
}

// skipHeredocBodies skips the bodies of the queued here-documents, which
// start right after the newline just lexed, and extends that newline token
// over them.
func (l *rcLexer) skipHeredocBodies() error {
	for _, h := range l.heredocs {
		for {
			if l.pos == len(l.src) {
				return l.fail(h.start, fmt.Sprintf("here-document without its closing %q line", h.delim))
			}
			line, rest, found := strings.Cut(l.src[l.pos:], "\n")
			l.pos = len(l.src) - len(rest)
			if !found {
				l.pos = len(l.src)
			}
			if h.stripTabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				break
			}
		}
	}
	l.heredocs = nil
	l.tokens[len(l.tokens)-1].end = l.pos
	return nil
}

func (l *rcLexer) word() error {
	start := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if isRcBlank(c) || c == '\n' || isRcOperatorChar(c) {
			break
		}
		if err := l.wordUnit(); err != nil {
			return err
		}
	}
	l.emit(rcWord, start)
	return nil
}

// wordUnit consumes one unquoted element of a word: an escaped character,
// a quoted string, a substitution, or a plain character.
func (l *rcLexer) wordUnit() error {
	switch l.src[l.pos] {
	case '\\':
		l.pos = min(l.pos+2, len(l.src))
	case '\'':
		return l.singleQuoted()
	case '"':
		return l.doubleQuoted()
	case '`':
		return l.backquoted()
	case '$':
		return l.dollar(false)
	default:
		l.pos++
	}
	return nil
}

func (l *rcLexer) singleQuoted() error {
	end := strings.IndexByte(l.src[l.pos+1:], '\'')
	if end < 0 {
		return l.fail(l.pos, "unterminated single quote")
	}
	l.pos += end + 2
	return nil
}

// dollar consumes a '$' and the substitution or $'...' string it starts.
// Inside double quotes $'...' is not special.
func (l *rcLexer) dollar(inDouble bool) error {
	rest := l.src[l.pos:]
	switch {
	case strings.HasPrefix(rest, "$("):
		return l.commandSubstitution()
	case strings.HasPrefix(rest, "${"):
		return l.parameterExpansion(inDouble)
	case !inDouble && strings.HasPrefix(rest, "$'"):
		return l.dollarSingleQuoted()
	}
	l.pos++
	return nil
}

// dollarSingleQuoted consumes $'...', in which a backslash escapes the
// next character (\' included).
func (l *rcLexer) dollarSingleQuoted() error {
	return l.until(l.pos+2, "unterminated $'...' string", func(c byte) (bool, error) {
		return c == '\'', nil
	})
}

func (l *rcLexer) doubleQuoted() error {
	return l.until(l.pos+1, "unterminated double quote", func(c byte) (bool, error) {
		switch c {
		case '"':
			return true, nil
		case '`':
			return false, l.backquoted()
		case '$':
			return false, l.dollar(true)
		}
		return false, nil
	})
}

// backquoted consumes `...`: it ends at the first unescaped backquote.
func (l *rcLexer) backquoted() error {
	return l.until(l.pos+1, "unterminated backquote", func(c byte) (bool, error) {
		return c == '`', nil
	})
}

// parameterExpansion consumes ${...}. Its word may hold quotes and further
// substitutions; inside double quotes a single quote is literal.
func (l *rcLexer) parameterExpansion(inDouble bool) error {
	return l.until(l.pos+2, "unterminated ${...}", func(c byte) (bool, error) {
		switch c {
		case '}':
			return true, nil
		case '\'':
			if inDouble {
				return false, nil
			}
			return false, l.singleQuoted()
		case '"':
			return false, l.doubleQuoted()
		case '`':
			return false, l.backquoted()
		case '$':
			return false, l.dollar(inDouble)
		}
		return false, nil
	})
}

// commandSubstitution consumes $(...) (and $((...))), balancing
// parentheses around nested quotes and substitutions. A case statement or
// a here-document inside is refused: their unbalanced ')' and bodies are
// not modelled.
func (l *rcLexer) commandSubstitution() error {
	depth, wordStart := 1, true
	return l.until(l.pos+2, "unterminated $(...)", func(c byte) (bool, error) {
		atWord := wordStart
		wordStart = isRcBlank(c) || strings.IndexByte("\n;&|()", c) >= 0
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			return depth == 0, nil
		case '\'':
			return false, l.singleQuoted()
		case '"':
			return false, l.doubleQuoted()
		case '`':
			return false, l.backquoted()
		case '$':
			return false, l.dollar(false)
		case '#':
			if atWord {
				l.skipComment() // stops at the newline, which is scanned next
			}
		case '<':
			if strings.HasPrefix(l.src[l.pos:], "<<") {
				return false, l.fail(l.pos, "here-document inside $(...) is not supported")
			}
		case 'c':
			if atWord && isRcKeywordAt(l.src[l.pos:], "case") {
				return false, l.fail(l.pos, "case inside $(...) is not supported")
			}
		}
		return false, nil
	})
}

// until scans from from until step reports the closing character. A
// backslash always escapes the next character. step sees every other
// character at l.pos; when it consumes a nested construct it advances
// l.pos itself (and returns that construct's error), otherwise until moves
// past the character. Reaching the end of the source fails with what at
// the construct's start.
func (l *rcLexer) until(from int, what string, step func(c byte) (bool, error)) error {
	start := l.pos
	l.pos = from
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '\\' {
			l.pos += 2
			continue
		}
		before := l.pos
		done, err := step(c)
		if err != nil {
			return err
		}
		if l.pos == before {
			l.pos++
		}
		if done {
			return nil
		}
	}
	return l.fail(start, what)
}

// isRcKeywordAt reports whether s starts with the reserved word kw as a
// whole word.
func isRcKeywordAt(s, kw string) bool {
	if !strings.HasPrefix(s, kw) {
		return false
	}
	rest := s[len(kw):]
	return rest == "" || isRcBlank(rest[0]) || strings.IndexByte("\n;&|()", rest[0]) >= 0
}

// rcLineIndex maps byte offsets of a source to 0-based physical lines.
type rcLineIndex []int

func newRcLineIndex(src string) rcLineIndex {
	starts := rcLineIndex{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// line returns the 0-based physical line holding byte offset off.
func (x rcLineIndex) line(off int) int {
	return sort.Search(len(x), func(i int) bool { return x[i] > off }) - 1
}
