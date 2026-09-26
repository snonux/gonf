package service

// Finding and rewriting one variable's assignments in an rc.conf-style
// file, on top of the lexer in rcconf_lex.go. The rules fail closed: an
// assignment whose effect cannot be told for sure (inside if/while/case/
// {...}/a subshell, in front of a command, via eval or ${VAR:=...}) is an
// error rather than overlooked, and a rewrite that would touch anything
// besides the assignment itself is refused rather than done.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// errRcUnmanaged marks an assignment WithFlags cannot read or rewrite
// safely; the error names its line.
var errRcUnmanaged = errors.New("not an assignment WithFlags can manage")

// rcFoundAssignment is one assignment to the managed variable.
type rcFoundAssignment struct {
	value string // raw shell text after "NAME="
	line  int    // 0-based physical line of the assignment word
	// first and end bound the physical lines [first, end) of its logical
	// line; alone reports that the assignment is all that logical line
	// holds (besides blanks and a comment), so replacing those lines
	// removes nothing else.
	first, end int
	alone      bool
}

// findRcAssignments returns every assignment to name in src, in order.
func findRcAssignments(src, name string) ([]rcFoundAssignment, error) {
	tokens, err := lexRc(src)
	if err != nil {
		return nil, err
	}
	w := rcWalker{name: name, lines: newRcLineIndex(src)}
	start := 0 // byte offset where the current logical line starts
	var logical []rcToken
	for _, t := range tokens {
		if t.kind != rcNewline {
			logical = append(logical, t)
			continue
		}
		if err := w.logicalLine(logical, start, t.start); err != nil {
			return nil, err
		}
		start, logical = t.end, nil
	}
	if len(logical) > 0 {
		if err := w.logicalLine(logical, start, len(src)-1); err != nil {
			return nil, err
		}
	}
	return w.found, nil
}

// rcWalker follows the compound-command nesting of a file across its
// logical lines and collects the assignments to name.
type rcWalker struct {
	name      string
	lines     rcLineIndex
	depth     int // open if/while/until/for/case/{/( constructs
	subshells int // open ( subshells, so a case pattern's ) is not one
	found     []rcFoundAssignment
}

// logicalLine walks the tokens of the logical line spanning byte offsets
// [start, last].
func (w *rcWalker) logicalLine(tokens []rcToken, start, last int) error {
	at := rcCommandAt{first: w.lines.line(start), end: w.lines.line(last) + 1, alone: len(tokens) == 1}
	var words []rcToken
	redirectTarget := false
	prevOp := ""
	for _, t := range tokens {
		switch {
		case t.kind == rcWord && redirectTarget:
			redirectTarget = false // a file name or here-document delimiter
		case t.kind == rcWord:
			words = append(words, t)
		case strings.ContainsAny(t.text, "<>"):
			redirectTarget = true
		default: // a command separator or a parenthesis
			at.listed = isRcListOperator(prevOp) || isRcListOperator(t.text)
			if err := w.command(words, at); err != nil {
				return err
			}
			w.parenthesis(t.text, len(words) == 0)
			words, prevOp = nil, t.text
		}
	}
	at.listed = isRcListOperator(prevOp)
	return w.command(words, at)
}

// rcCommandAt locates a simple command: the physical lines [first, end) of
// its logical line; alone reports that the logical line is one word;
// listed that the command is part of an && or || list, a pipeline or a
// background job, where an assignment may not run or not persist.
type rcCommandAt struct {
	first, end    int
	alone, listed bool
}

func isRcListOperator(op string) bool {
	return op == "&&" || op == "||" || op == "|" || op == "&"
}

// parenthesis tracks subshells: "(" in command position opens one; any
// other "(" belongs to a function definition, and a ")" that closes no
// subshell ends a case pattern.
func (w *rcWalker) parenthesis(op string, commandPosition bool) {
	switch {
	case op == "(" && commandPosition:
		w.subshells++
		w.depth++
	case op == ")" && w.subshells > 0:
		w.subshells--
		w.depth--
	}
}

// command walks one simple command: reserved words in command position,
// then its leading assignments, then an optional command and arguments.
func (w *rcWalker) command(words []rcToken, at rcCommandAt) error {
	words, done := w.reservedWords(words)
	if done {
		return w.refuseMentions(words)
	}
	i := 0
	for i < len(words) && rcAssignedName(words[i].text) != "" {
		i++
	}
	// Clip: export's assignments are appended below and must not
	// overwrite rest, which shares the backing array.
	assignments, rest := slices.Clip(words[:i]), words[i:]
	if len(rest) > 0 && rest[0].text != "export" && rest[0].text != "readonly" {
		// NAME=value cmd sets NAME for cmd alone (for most commands).
		for _, a := range assignments {
			if rcAssignedName(a.text) == w.name {
				return w.unmanaged(a, "is set only for the command after it")
			}
		}
		for _, a := range assignments {
			if err := w.refuseExpansionAssign(a); err != nil {
				return err
			}
		}
		return w.refuseMentions(rest)
	}
	if len(rest) > 0 { // export/readonly NAME=value ...
		for _, arg := range rest[1:] {
			if rcAssignedName(arg.text) != "" {
				assignments = append(assignments, arg)
			} else if err := w.refuseMentions([]rcToken{arg}); err != nil {
				return err
			}
		}
		at.alone = false
	}
	at.alone = at.alone && len(assignments) == 1
	for _, a := range assignments {
		if err := w.assignment(a, at); err != nil {
			return err
		}
	}
	return nil
}

// reservedWords strips the reserved words in command position off words,
// tracking the nesting they open and close. done reports that the rest is
// no command (the word list of for, the subject of case).
func (w *rcWalker) reservedWords(words []rcToken) (rest []rcToken, done bool) {
	for len(words) > 0 {
		switch words[0].text {
		case "if", "while", "until", "{":
			w.depth++
		case "fi", "done", "esac", "}":
			w.depth = max(w.depth-1, 0)
		case "for", "case":
			w.depth++
			return words[1:], true
		case "then", "do", "else", "elif", "!":
		default:
			return words, false
		}
		words = words[1:]
	}
	return nil, false
}

// assignment records an assignment word, when it assigns name.
func (w *rcWalker) assignment(word rcToken, at rcCommandAt) error {
	if err := w.refuseExpansionAssign(word); err != nil {
		return err
	}
	if rcAssignedName(word.text) != w.name {
		return nil
	}
	if w.depth > 0 {
		return w.unmanaged(word, "is set inside if/while/until/for/case/{...}/(...), so whether it takes effect is unknown")
	}
	if at.listed {
		return w.unmanaged(word, "is set in an &&/|| list, a pipeline or a background job, so whether it takes effect is unknown")
	}
	w.found = append(w.found, rcFoundAssignment{
		value: word.text[len(w.name)+1:], line: w.lines.line(word.start),
		first: at.first, end: at.end, alone: at.alone,
	})
	return nil
}

// refuseMentions refuses command words and arguments that may assign name
// (eval name=..., a reserved word this walker does not model, ...).
func (w *rcWalker) refuseMentions(words []rcToken) error {
	for _, word := range words {
		if rcMentionsAssign(word.text, w.name, false) {
			return w.unmanaged(word, "may be assigned by this command")
		}
	}
	return nil
}

// refuseExpansionAssign refuses an assignment whose value assigns name by
// ${name=...} or ${name:=...}.
func (w *rcWalker) refuseExpansionAssign(word rcToken) error {
	if rcMentionsAssign(word.text, w.name, true) {
		return w.unmanaged(word, "is assigned by a ${...=...} expansion")
	}
	return nil
}

func (w *rcWalker) unmanaged(word rcToken, why string) error {
	return fmt.Errorf("line %d: %s %s: %w", w.lines.line(word.start)+1, w.name, why, errRcUnmanaged)
}

// rcAssignedName returns the variable an assignment word assigns, or "".
func rcAssignedName(word string) string {
	name, _, ok := strings.Cut(word, "=")
	if !ok || !isShellName(name) {
		return ""
	}
	return name
}

// isShellName reports whether s is a sh(1) variable name.
func isShellName(s string) bool {
	if s == "" || isShellDigit(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isShellNameChar(s[i]) {
			return false
		}
	}
	return true
}

func isShellDigit(c byte) bool { return c >= '0' && c <= '9' }

func isShellNameChar(c byte) bool {
	return c == '_' || isShellDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

// rcMentionsAssign reports whether text contains name, as a whole name,
// followed by "=" or ":=". With inBraces only a "${name" occurrence counts
// (an expansion that assigns).
func rcMentionsAssign(text, name string, inBraces bool) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		at, after := i+j, i+j+len(name)
		i = at + 1
		if at > 0 && isShellNameChar(text[at-1]) {
			continue
		}
		if inBraces && !strings.HasSuffix(text[:at], "${") {
			continue
		}
		rest := text[after:]
		if strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, ":=") {
			return true
		}
	}
}
