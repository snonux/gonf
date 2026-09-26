package service

// Finding and rewriting one variable's assignments in an rc.conf-style
// file, on top of the lexer in rcconf_lex.go. The rules fail closed and
// refuse broadly rather than model more of sh(1): an assignment whose
// effect cannot be told for sure is an error naming its line, never
// overlooked, and a rewrite replaces nothing but assignment words, then
// must read back as exactly what was asked for before it is written.

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
	value      string // raw shell text after "NAME="
	start, end int    // byte offsets of the assignment word
	line       int    // 0-based physical line of the assignment word
	// first and lastEnd bound the physical lines [first, lastEnd) of its
	// logical line; alone reports that the word is all that logical line
	// holds (besides blanks and a comment).
	first, lastEnd int
	alone          bool
}

// rcScan is what scanRcAssignments learns about one file.
type rcScan struct {
	found []rcFoundAssignment // every assignment to the variable, in order
	// includesOther reports a ., source or eval command other than
	// rc.conf's own ". /etc/defaults/rc.conf": a file (or string) that
	// may set the variable without this scan seeing it.
	includesOther bool
	// namedHazard reports an unset, read, getopts or for naming the
	// variable, which may change it without an assignment.
	namedHazard bool
	// includesDefaults reports rc.conf's own ". /etc/defaults/rc.conf".
	includesDefaults bool
}

// unseen reports that the file may set the variable without an
// assignment this scan reads.
func (s rcScan) unseen() bool { return s.includesOther || s.namedHazard }

// rcDefaultsInclude is the file rc.conf's stock header sources first.
const rcDefaultsInclude = "/etc/defaults/rc.conf"

// scanRcAssignments finds every assignment to name in src.
func scanRcAssignments(src, name string) (rcScan, error) {
	tokens, err := lexRc(src)
	if err != nil {
		return rcScan{}, err
	}
	w := rcWalker{name: name, lines: newRcLineIndex(src)}
	start := 0 // byte offset where the current logical line starts
	var logical []rcToken
	for _, t := range tokens {
		switch {
		case t.kind != rcNewline:
			logical = append(logical, t)
			continue
		case endsInListOperator(logical):
			continue // sh reads on after a trailing &&, || or |
		}
		if err := w.logicalLine(logical, start, t.start); err != nil {
			return rcScan{}, err
		}
		start, logical = t.end, nil
	}
	if endsInListOperator(logical) {
		last := logical[len(logical)-1]
		return rcScan{}, fmt.Errorf("line %d: the file ends after %s: %w", w.lines.line(last.start)+1, last.text, errRcSyntax)
	}
	if len(logical) > 0 {
		if err := w.logicalLine(logical, start, len(src)-1); err != nil {
			return rcScan{}, err
		}
	}
	return rcScan{
		found: w.found, includesOther: w.includesOther, namedHazard: w.namedHazard, includesDefaults: w.includesDefaults,
	}, w.checkHazards()
}

// endsInListOperator reports whether tokens end in &&, || or |, after
// which sh(1) continues the command on the next line.
func endsInListOperator(tokens []rcToken) bool {
	if len(tokens) == 0 {
		return false
	}
	last := tokens[len(tokens)-1]
	return last.kind == rcOperator && (last.text == "&&" || last.text == "||" || last.text == "|")
}

// rcWalker follows the nesting of a file across its logical lines and
// collects the assignments to name.
type rcWalker struct {
	name  string
	lines rcLineIndex
	depth int // open if/while/until/for/{ constructs
	// opaque is set, for the rest of the file, by the first function
	// definition, case statement, subshell or !-command: past it, the
	// nesting of a later assignment is not tracked, so it is refused.
	opaque bool
	// hazards are commands that may change name behind the walker's back
	// (., source, eval; unset, read, getopts or for naming it); one after
	// the last assignment makes the value unknowable.
	hazards                                      []rcToken
	includesOther, namedHazard, includesDefaults bool
	found                                        []rcFoundAssignment
}

// logicalLine walks the tokens of the logical line spanning byte offsets
// [start, last].
func (w *rcWalker) logicalLine(tokens []rcToken, start, last int) error {
	at := rcCommandAt{first: w.lines.line(start), lastEnd: w.lines.line(last) + 1, alone: len(tokens) == 1}
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
			if t.text == "(" || t.text == ")" || t.text == ";;" {
				w.opaque = true // a subshell, a function definition or a case
			}
			words, prevOp = nil, t.text
		}
	}
	at.listed = isRcListOperator(prevOp)
	return w.command(words, at)
}

// rcCommandAt locates a simple command: the physical lines [first,
// lastEnd) of its logical line; alone reports that the logical line is one
// word; listed that the command is part of an && or || list, a pipeline or
// a background job, where an assignment may not run or not persist.
type rcCommandAt struct {
	first, lastEnd int
	alone, listed  bool
}

func isRcListOperator(op string) bool {
	return op == "&&" || op == "||" || op == "|" || op == "&"
}

// command walks one simple command: reserved words in command position,
// then its leading assignments, then an optional command and arguments.
func (w *rcWalker) command(words []rcToken, at rcCommandAt) error {
	words, done := w.reservedWords(words)
	if done {
		return w.refuseMentions(words)
	}
	i := 0
	for i < len(words) && rcAssignedName(words[i].joined) != "" {
		i++
	}
	// Clip: export's assignments are appended below and must not
	// overwrite rest, which shares the backing array.
	assignments, rest := slices.Clip(words[:i]), words[i:]
	for _, a := range assignments {
		if err := w.refuseMentionsAfter(a, len(rcAssignedName(a.joined))+1); err != nil {
			return err
		}
	}
	if len(rest) > 0 {
		switch rcUnquote(rest[0].joined) {
		case "export", "readonly":
			for _, arg := range rest[1:] {
				if rcAssignedName(arg.joined) == "" {
					if err := w.refuseMentions([]rcToken{arg}); err != nil {
						return err
					}
					continue
				}
				if err := w.refuseMentionsAfter(arg, len(rcAssignedName(arg.joined))+1); err != nil {
					return err
				}
				assignments = append(assignments, arg)
			}
			at.alone = false
		default:
			w.noteCommand(rest)
			// NAME=value cmd sets NAME for cmd alone (for most commands).
			for _, a := range assignments {
				if rcAssignedName(a.joined) == w.name {
					return w.unmanaged(a, "is set only for the command after it")
				}
			}
			return w.refuseMentions(rest)
		}
	}
	at.alone = at.alone && len(assignments) == 1
	for _, a := range assignments {
		if err := w.assignment(a, at); err != nil {
			return err
		}
	}
	return nil
}

// noteCommand records a command that may change name without assigning
// it in a way the walker reads: ., source and eval (also behind
// "command"), and unset, read or getopts naming it.
func (w *rcWalker) noteCommand(command []rcToken) {
	for len(command) > 1 && rcUnquote(command[0].joined) == "command" {
		command = command[1:]
		for len(command) > 1 && strings.HasPrefix(rcUnquote(command[0].joined), "-") {
			command = command[1:]
		}
	}
	args := command[1:]
	switch rcUnquote(command[0].joined) {
	case ".", "source", "eval":
		w.hazards = append(w.hazards, command[0])
		if len(args) == 1 && rcUnquote(args[0].joined) == rcDefaultsInclude && rcUnquote(command[0].joined) == "." {
			w.includesDefaults = true
		} else {
			w.includesOther = true
		}
	case "unset", "read", "getopts":
		for _, arg := range args {
			if rcUnquote(arg.joined) == w.name {
				w.hazards = append(w.hazards, command[0])
				w.namedHazard = true
				return
			}
		}
	}
}

// checkHazards refuses a hazard after the last assignment found. One
// before it (like rc.conf's leading ". /etc/defaults/rc.conf") is
// overridden by the assignment.
func (w *rcWalker) checkHazards() error {
	if len(w.found) == 0 {
		return nil
	}
	last := w.found[len(w.found)-1]
	for _, h := range w.hazards {
		if h.start > last.start {
			return w.unmanaged(h, fmt.Sprintf("may be changed by this %s command after its assignment on line %d",
				rcUnquote(h.joined), last.line+1))
		}
	}
	return nil
}

// reservedWords strips the reserved words in command position off words,
// tracking the nesting they open and close. done reports that the rest is
// no command (the word list of for, the subject of case). A quoted word is
// never a reserved word.
func (w *rcWalker) reservedWords(words []rcToken) (rest []rcToken, done bool) {
	for len(words) > 0 {
		switch words[0].joined {
		case "if", "while", "until", "{":
			w.depth++
		case "fi", "done", "}":
			w.depth = max(w.depth-1, 0)
		case "for":
			w.depth++
			if len(words) > 1 && rcUnquote(words[1].joined) == w.name {
				w.hazards = append(w.hazards, words[0])
				w.namedHazard = true
			}
			return words[1:], true
		case "case", "esac", "!":
			w.opaque = true
			if words[0].joined == "case" {
				return words[1:], true
			}
		case "then", "do", "else", "elif":
		default:
			return words, false
		}
		words = words[1:]
	}
	return nil, false
}

// assignment records an assignment word, when it assigns name.
func (w *rcWalker) assignment(word rcToken, at rcCommandAt) error {
	if rcAssignedName(word.joined) != w.name {
		return nil
	}
	switch {
	case w.depth > 0:
		return w.unmanaged(word, "is set inside if/while/until/for/{...}, so whether it takes effect is unknown")
	case w.opaque:
		return w.unmanaged(word, "is set after a function, case, subshell or ! command, so whether it takes effect is unknown")
	case at.listed:
		return w.unmanaged(word, "is set in an &&/|| list, a pipeline or a background job, so whether it takes effect is unknown")
	}
	w.found = append(w.found, rcFoundAssignment{
		// The name holds no quote, so its "=" is the first in the raw text
		// too (a backslash-newline inside the name is skipped).
		value: word.text[strings.IndexByte(word.text, '=')+1:],
		start: word.start, end: word.end, line: w.lines.line(word.start),
		first: at.first, lastEnd: at.lastEnd, alone: at.alone,
	})
	return nil
}

// refuseMentions refuses words that may assign name anywhere in their
// text (eval name=..., $((name=1)), a construct this walker does not
// model, ...).
func (w *rcWalker) refuseMentions(words []rcToken) error {
	for _, word := range words {
		if err := w.refuseMentionsAfter(word, 0); err != nil {
			return err
		}
	}
	return nil
}

// refuseMentionsAfter refuses word when its text from byte from on (past
// an assignment's own "NAME=") may assign name: ${name:=...},
// $((name=1)), $((name+=1)), a quoted "name=..." handed to something.
func (w *rcWalker) refuseMentionsAfter(word rcToken, from int) error {
	if rcMentionsAssign(word.joined[from:], w.name) {
		return w.unmanaged(word, "may be assigned inside this word")
	}
	return nil
}

func (w *rcWalker) unmanaged(word rcToken, why string) error {
	return fmt.Errorf("line %d: %s %s: %w", w.lines.line(word.start)+1, w.name, why, errRcUnmanaged)
}

// rcUnquote removes quote characters and backslashes from a bare command
// word, enough to see that "." or \. is the dot command.
func rcUnquote(word string) string {
	return strings.NewReplacer(`\`, "", `'`, "", `"`, "").Replace(word)
}

// replaceRcAssignment returns content with name set to value: the first
// assignment's word becomes name='value' in place, later ones alone on
// their logical line are dropped with that line, and any other later one
// is set the same in place; with none, the assignment is appended. Only
// assignment words (and dropped duplicate lines) change, so export, other
// statements and comments sharing a line survive. It fails closed: it
// refuses an assignment spanning several lines with a value it cannot
// evaluate, and a result that does not read back with every assignment to
// name evaluating to exactly value.
func replaceRcAssignment(content, name, value string) (string, error) {
	scan, err := scanRcAssignments(content, name)
	if err != nil {
		return "", err
	}
	found := scan.found
	if err := refuseIntermediateReads(content, name, found); err != nil {
		return "", err
	}
	assignment := name + "=" + shellQuote(value)
	var updated string
	if len(found) == 0 {
		updated = content
		if updated != "" && !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		updated += assignment + "\n"
	} else {
		edits, err := rcAssignmentEdits(content, found, assignment)
		if err != nil {
			return "", err
		}
		updated = applyRcEdits(content, edits)
	}
	if err := checkRcRewrite(updated, name, value); err != nil {
		return "", err
	}
	return updated, nil
}

// refuseIntermediateReads refuses a rewrite when anything between the
// first and the last assignment (besides the assignment words themselves)
// reads the variable: rewriting the first assignment would change what it
// reads. The raw text is searched, comments and here-documents included,
// erring on the side of refusing.
func refuseIntermediateReads(content, name string, found []rcFoundAssignment) error {
	for i := 0; i+1 < len(found); i++ {
		between := content[found[i].end:found[i+1].start]
		if at := rcReference(between, name); at >= 0 {
			lines := newRcLineIndex(content)
			return fmt.Errorf("line %d: %s is read between its assignments on lines %d and %d, so rewriting would change what is read there: %w",
				lines.line(found[i].end+at)+1, name, found[0].line+1, found[len(found)-1].line+1, errRcUnmanaged)
		}
	}
	return nil
}

// rcReference returns the offset of the first $name or ${name in text, or
// -1.
func rcReference(text, name string) int {
	for i := 0; ; {
		j := strings.IndexByte(text[i:], '$')
		if j < 0 {
			return -1
		}
		at := i + j
		i = at + 1
		rest := strings.TrimPrefix(text[at+1:], "{")
		if strings.HasPrefix(rest, name) && (len(rest) == len(name) || !isShellNameChar(rest[len(name)])) {
			return at
		}
	}
}

// rcEdit replaces the bytes [start, end) of a source with text.
type rcEdit struct {
	start, end int
	text       string
}

// rcAssignmentEdits returns the edits replaceRcAssignment makes, in order.
func rcAssignmentEdits(content string, found []rcFoundAssignment, assignment string) ([]rcEdit, error) {
	lines := newRcLineIndex(content)
	edits := make([]rcEdit, 0, len(found))
	for i, a := range found {
		if _, parsed := parseShellWord(a.value); !parsed && strings.Contains(content[a.start:a.end], "\n") {
			return nil, fmt.Errorf("line %d: %s spans several lines with a value WithFlags cannot evaluate: %w",
				a.line+1, rcAssignedName(strings.ReplaceAll(content[a.start:a.end], "\\\n", "")), errRcUnmanaged)
		}
		if i > 0 && a.alone {
			edits = append(edits, rcEdit{start: lines.start(a.first, content), end: lines.start(a.lastEnd, content)})
			continue
		}
		edits = append(edits, rcEdit{start: a.start, end: a.end, text: assignment})
	}
	return edits, nil
}

// applyRcEdits applies ordered, non-overlapping edits to src.
func applyRcEdits(src string, edits []rcEdit) string {
	var b strings.Builder
	b.Grow(len(src))
	pos := 0
	for _, e := range edits {
		b.WriteString(src[pos:e.start])
		b.WriteString(e.text)
		pos = e.end
	}
	b.WriteString(src[pos:])
	return b.String()
}

// checkRcRewrite is replaceRcAssignment's safety net: the rewritten text
// must read back cleanly, with every assignment to name evaluating to
// exactly value.
func checkRcRewrite(updated, name, value string) error {
	scan, err := scanRcAssignments(updated, name)
	if err != nil {
		return fmt.Errorf("the rewrite would not read back (%v): %w", err, errRcUnmanaged)
	}
	found := scan.found
	if len(found) == 0 {
		return fmt.Errorf("the rewrite would not read back an assignment to %s: %w", name, errRcUnmanaged)
	}
	for _, a := range found {
		if got, ok := parseShellWord(a.value); !ok || got != value {
			return fmt.Errorf("line %d: the rewrite would read back %s=%s: %w", a.line+1, name, a.value, errRcUnmanaged)
		}
	}
	return nil
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
// followed by an assignment operator: "=" (not "=="), ":=", or an
// arithmetic one such as "+=" or "<<=". It errs on the side of reporting:
// "<=" and ">=" count too.
func rcMentionsAssign(text, name string) bool {
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
		rest := strings.TrimLeft(text[after:], ":+-*/%&|^<>")
		if strings.HasPrefix(rest, "=") && !strings.HasPrefix(text[after:], "==") {
			return true
		}
	}
}
