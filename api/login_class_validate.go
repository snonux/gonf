package api

import (
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/options"
)

// loginClassNameRe admits class names that are safe both as a getcap(3)
// record name (no ':' or '|' separators) and as a single file name directly
// under /etc/login.conf.d (no '/', and a leading alphanumeric rules out "."
// and "..").
var loginClassNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validateLoginClassName aborts registration for a class name that could not
// be looked up as its own fragment: login_getclass(3) opens
// /etc/login.conf.d/<class> verbatim, so path separators, dot names, getcap
// separators, and a ".db" suffix (the compiled-database name getcap prefers)
// are rejected.
func validateLoginClassName(class string) {
	if !loginClassNameRe.MatchString(class) || strings.HasSuffix(class, ".db") {
		logger.Fatal("LoginClass: invalid class name %q; want letters, digits, '.', '_' or '-', "+
			"starting with a letter or digit and not ending in .db", class)
	}
}

// loginClassProbe records what the caller's file options would do to the
// fragment's content without registering anything. Embedding *file.File keeps
// every other file option (mode, owner, DependsOn, templates, ...) accepted
// through promoted setters; the content-related setters are shadowed so the
// probe learns the content source that will actually be installed.
type loginClassProbe struct {
	*file.File
	source     string // last WithSource, when it won over WithContent
	content    string // last WithContent, when it won over WithSource
	sourceSet  bool
	contentSet bool
	absent     bool
	lineEdit   bool
}

func (p *loginClassProbe) SetSource(s string) {
	p.source, p.sourceSet, p.contentSet = s, true, false
}

func (p *loginClassProbe) SetContent(c string) {
	p.content, p.contentSet, p.sourceSet = c, true, false
}

func (p *loginClassProbe) SetAbsent()               { p.absent = true }
func (p *loginClassProbe) SetAddLine(string)        { p.lineEdit = true }
func (p *loginClassProbe) SetRemoveLine(string)     { p.lineEdit = true }
func (p *loginClassProbe) AddLines(...string)       { p.lineEdit = true }
func (p *loginClassProbe) RemoveLines(...string)    { p.lineEdit = true }
func (p *loginClassProbe) hasContentOverride() bool { return p.sourceSet || p.contentSet }

// inspectLoginClassOptions applies opts to a probe. Line edits are refused:
// the fragment is owned whole, so partial edits of it make no sense.
func inspectLoginClassOptions(class string, opts []options.FileOption) *loginClassProbe {
	p := &loginClassProbe{File: &file.File{}}
	for _, o := range opts {
		o.Apply(p)
	}
	if p.lineEdit {
		logger.Fatal("LoginClass %q: WithLine(s)/WithoutLine(s) are not supported; the fragment is owned as a whole file", class)
	}
	return p
}

// installedContent returns the text that will be installed and whether it is
// known on the controller: a caller WithContent/WithSource overrides src
// (they are applied after it), and an unreadable source is left to the file
// resource to report. src is LoginClass's already-expanded default source;
// a caller's WithSource path is read verbatim, exactly as the file resource
// reads it (WithSource does not expand "~"), so validation never inspects a
// different file than the one installed.
func (p *loginClassProbe) installedContent(src string) (string, bool) {
	switch {
	case p.contentSet:
		return p.content, true
	case p.sourceSet:
		src = p.source
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// validateLoginClassContent checks the lookup contract that makes a fragment
// effective: OpenBSD only consults /etc/login.conf.d/<class> for <class>, so
// some record in it must carry that name (canonical name or '|' alias).
// Content unknown on the controller, or record names that are still
// templates ("{{"), cannot be judged before rendering and are accepted.
func validateLoginClassContent(class string, content string, known bool) {
	if !known {
		return
	}
	names := loginClassRecordNames(content)
	if slices.Contains(names, class) {
		return
	}
	if slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "{{") }) {
		return
	}
	logger.Fatal("LoginClass: fragment for %q defines classes %q but not %q; OpenBSD reads %s/%s only when "+
		"looking up %q, so the fragment would never be used", class, names, class, loginClassDir, class, class)
}

// loginClassRecordNames returns every name of every getcap(3) record in
// content. Physical lines ending in '\' continue a logical record; blank
// records and '#' comments are skipped; a record's names are the
// '|'-separated fields before its first ':'.
func loginClassRecordNames(content string) []string {
	var names []string
	for _, record := range loginClassRecords(content) {
		field, _, _ := strings.Cut(record, ":")
		for _, n := range strings.Split(field, "|") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	return names
}

// loginClassRecords joins continuation lines into logical getcap records and
// drops comments and blank records.
func loginClassRecords(content string) []string {
	var records []string
	var cur strings.Builder
	flush := func() {
		rec := strings.TrimSpace(cur.String())
		cur.Reset()
		if rec != "" && !strings.HasPrefix(rec, "#") {
			records = append(records, rec)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if body, cont := strings.CutSuffix(line, `\`); cont {
			cur.WriteString(body)
			continue
		}
		cur.WriteString(line)
		flush()
	}
	flush()
	return records
}
