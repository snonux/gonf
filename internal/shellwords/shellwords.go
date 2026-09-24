// Package shellwords splits a command line into argv words the way a POSIX
// shell tokenizes a simple command, without ever running a shell: quotes
// and backslashes group and escape, nothing is expanded. api.Sh uses it.
//
// Because nothing is interpreted, the characters a shell would give a
// meaning to (pipes, redirections, lists, subshells, expansions, globs,
// comments, a leading ~) are refused when they appear unquoted, instead of
// silently becoming literal argv words that do something else than the
// line reads like. Quote them to pass them literally.
package shellwords

import (
	"errors"
	"fmt"
	"strings"
)

// shellSpecial are the unquoted characters Split refuses; '#' and '~' only
// at the start of a word, where a shell reads a comment or a home directory.
const shellSpecial = "|&;<>()$`*?[#~"

// Split returns the words of line. Words are separated by blanks (space,
// tab); 'single quotes' keep everything literally, "double quotes" keep
// everything but a backslash before $ ` " or \, and an unquoted backslash
// escapes the next character. An empty quoted word (a bare pair of single
// or double quotes) is kept as an empty argument. An empty line, an
// unterminated quote, a trailing backslash, a line break, an unquoted shell
// metacharacter (shellSpecial), or an unescaped $ or ` inside double quotes
// (where a shell would expand it) is an error.
func Split(line string) ([]string, error) {
	if strings.ContainsAny(line, "\n\r") {
		return nil, errors.New("command must be a single line")
	}
	s := splitter{line: line}
	if err := s.run(); err != nil {
		return nil, err
	}
	if len(s.words) == 0 {
		return nil, errors.New("command must not be empty")
	}
	return s.words, nil
}

// splitter is Split's scanner state: the words so far and the one being
// built (inWord distinguishes an empty quoted word from no word).
type splitter struct {
	line   string
	words  []string
	cur    strings.Builder
	inWord bool
}

// run scans the whole line into s.words.
func (s *splitter) run() error {
	for i := 0; i < len(s.line); i++ {
		c := s.line[i]
		var err error
		switch {
		case c == ' ' || c == '\t':
			s.endWord()
		case c == '\'':
			i, err = s.single(i)
		case c == '"':
			i, err = s.double(i)
		case c == '\\':
			if i+1 == len(s.line) {
				return errors.New("command ends with a backslash")
			}
			i++
			s.add(s.line[i])
		case (c == '#' || c == '~') && s.inWord:
			s.add(c) // only a word's first character starts a comment or a tilde expansion
		case strings.IndexByte(shellSpecial, c) >= 0:
			return fmt.Errorf("unquoted %q in command: Sh runs no shell, quote it to pass it literally", c)
		default:
			s.add(c)
		}
		if err != nil {
			return err
		}
	}
	s.endWord()
	return nil
}

// single consumes the single-quoted part starting at line[i] and returns
// the index of its closing quote.
func (s *splitter) single(i int) (int, error) {
	end := strings.IndexByte(s.line[i+1:], '\'')
	if end < 0 {
		return 0, errors.New("unterminated single quote in command")
	}
	s.inWord = true
	s.cur.WriteString(s.line[i+1 : i+1+end])
	return i + 1 + end, nil
}

// double consumes the double-quoted part starting at line[i] and returns
// the index of its closing quote.
func (s *splitter) double(i int) (int, error) {
	s.inWord = true
	for j := i + 1; j < len(s.line); j++ {
		switch c := s.line[j]; c {
		case '"':
			return j, nil
		case '$', '`':
			return 0, fmt.Errorf("%q in double quotes: Sh expands nothing, use single quotes or a backslash to pass it literally", c)
		case '\\':
			if j+1 < len(s.line) && strings.IndexByte("$`\"\\", s.line[j+1]) >= 0 {
				j++
			}
			s.cur.WriteByte(s.line[j])
		default:
			s.cur.WriteByte(c)
		}
	}
	return 0, errors.New("unterminated double quote in command")
}

// add appends c to the current word.
func (s *splitter) add(c byte) {
	s.inWord = true
	s.cur.WriteByte(c)
}

// endWord closes the current word, if any.
func (s *splitter) endWord() {
	if !s.inWord {
		return
	}
	s.words = append(s.words, s.cur.String())
	s.cur.Reset()
	s.inWord = false
}
