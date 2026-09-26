// Package gendesc generates DescX task-description companions from the doc
// comments of a recipe package's task methods (cmd/gonf-desc).
package gendesc

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/doc/comment"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultFile is the generated file's default name.
const DefaultFile = "desc_gen.go"

// method is one generated DescX companion.
type method struct {
	recv, name, desc string
}

// srcFile is one Go file of the package that some plain go build selects.
type srcFile struct {
	name   string
	ast    *ast.File
	builds targetSet // where go build selects the file
}

// Generate returns the formatted source of the generated file for the
// package in dir, or nil when no task method there needs a description.
// out, the generated file's name, is skipped when reading the package, and
// so is every file go build never selects without custom -tags: _x.go and
// .x.go, and files whose constraint no platform satisfies (ignore).
func Generate(dir, out string) ([]byte, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []srcFile
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == out ||
			strings.HasPrefix(n, "_") || strings.HasPrefix(n, ".") {
			continue
		}
		path := filepath.Join(dir, n)
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		builds, err := buildTargets(n, src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		if builds.none() {
			continue
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, srcFile{name: n, ast: f, builds: builds})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: no Go files", dir)
	}
	methods, err := collect(files)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if len(methods) == 0 {
		return nil, nil
	}
	// The generated file carries no constraint line and imports nothing,
	// so only out's name limits where it builds.
	outBuilds, err := buildTargets(out, []byte("package p\n"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if err := checkCoverage(files, methods, out, outBuilds); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return render(files[0].ast.Name.Name, methods)
}

// collect returns the DescX companions to generate, sorted by receiver and
// method name. A method defined in several build-constrained files (e.g.
// s_linux.go and s_freebsd.go) gets one companion, since the generated file
// has no build constraint line; differing doc comments are an error, as
// either description would be wrong on the other platform.
func collect(files []srcFile) ([]method, error) {
	type key struct{ recv, name string }
	have := map[key]bool{}
	var candidates []method
	for _, f := range files {
		for _, d := range f.ast.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
				continue
			}
			recv, ok := receiverName(fd.Recv.List[0].Type)
			if !ok {
				continue
			}
			name := fd.Name.Name
			have[key{recv, name}] = true
			if !isTask(fd) || fd.Doc == nil {
				continue
			}
			if desc := Describe(name, fd.Doc.Text()); desc != "" {
				candidates = append(candidates, method{recv: recv, name: name, desc: desc})
			}
		}
	}
	var out []method
	seen := map[key]string{}
	for _, m := range candidates {
		if have[key{m.recv, "Desc" + m.name}] {
			continue
		}
		k := key{m.recv, m.name}
		if prev, ok := seen[k]; ok {
			if prev != m.desc {
				return nil, fmt.Errorf("%s.%s has different doc comments in different files; write Desc%s by hand", m.recv, m.name, m.name)
			}
			continue
		}
		seen[k] = m.desc
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].recv != out[j].recv {
			return out[i].recv < out[j].recv
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// checkCoverage refuses a companion whose receiver type is missing on a
// target where the generated file out builds: there the companion names an
// undefined type and breaks the build. A type is present on a target when
// at least one file declaring it builds there, so a type split over
// complementary files (s_linux.go and a //go:build !linux file) is
// covered, and so is a linux-only package written to -o desc_linux.go. A
// refused companion belongs next to the type, hand-written, where it shares
// the type's constraint (a hand-written DescX wins, so none is generated).
// Every refused method is reported, in methods' order.
func checkCoverage(files []srcFile, methods []method, out string, outBuilds targetSet) error {
	present := map[string]targetSet{}
	declared := map[string][]string{} // type -> files declaring it
	for _, f := range files {
		for _, d := range f.ast.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok {
					continue
				}
				name := ts.Name.Name
				if present[name] == nil {
					present[name] = make(targetSet, len(targets))
				}
				present[name].union(f.builds)
				declared[name] = append(declared[name], f.name)
			}
		}
	}
	var refused []string
	for _, m := range methods {
		have, ok := present[m.recv]
		if !ok {
			continue // not declared at all: the package does not build anyway
		}
		missing := outBuilds.minus(have)
		if missing.none() {
			continue
		}
		where := fmt.Sprintf("%s builds everywhere, but %s (declared in %s) is missing on %s",
			out, m.recv, strings.Join(declared[m.recv], ", "), missing)
		if !outBuilds.all() {
			where = fmt.Sprintf("%s builds on %s, where %s (declared in %s) is missing",
				out, missing, m.recv, strings.Join(declared[m.recv], ", "))
		}
		refused = append(refused, fmt.Sprintf("%s.%s: %s; write Desc%s by hand next to %s", m.recv, m.name, where, m.name, m.recv))
	}
	switch len(refused) {
	case 0:
		return nil
	case 1:
		return errors.New(refused[0])
	}
	return fmt.Errorf("%d task methods refused:\n\t%s", len(refused), strings.Join(refused, "\n\t"))
}

// receiverName returns the type name of a method receiver (T or *T), and
// false for a generic receiver.
func receiverName(e ast.Expr) (string, bool) {
	if s, ok := e.(*ast.StarExpr); ok {
		e = s.X
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

// isTask reports whether fd is a task method as RegisterMethods sees it:
// exported, no parameters, no results, not a companion.
func isTask(fd *ast.FuncDecl) bool {
	name := fd.Name.Name
	if !ast.IsExported(name) || fd.Type.Params.NumFields() != 0 || fd.Type.Results.NumFields() != 0 {
		return false
	}
	if name == "Opts" {
		return false
	}
	for _, p := range []string{"Desc", "When", "Opts"} {
		if len(name) > len(p) && strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// Describe returns the task description a doc comment gives method name:
// its first sentence, without a leading "name " and without the final
// period, starting upper case. It is "" when nothing is left.
func Describe(name, text string) string {
	s := synopsis(text)
	s = strings.TrimPrefix(s, name+" ")
	s = strings.TrimSuffix(s, ".")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// synopsis is the first sentence of text, as go/doc reads it: up to the
// first period followed by a space that does not end a single upper-case
// letter (an initial), with line breaks joined into spaces.
func synopsis(text string) string {
	var p comment.Parser
	d := p.Parse(text)
	if len(d.Content) == 0 {
		return ""
	}
	para, ok := d.Content[0].(*comment.Paragraph)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, t := range para.Text {
		plainText(&b, t)
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '.' || s[i+1] != ' ' {
			continue
		}
		if i >= 1 && unicode.IsUpper(rune(s[i-1])) && (i == 1 || s[i-2] == ' ') {
			continue // an initial such as "J. Doe"
		}
		return s[:i+1]
	}
	return s
}

// plainText appends the text of a doc comment span to b.
func plainText(b *strings.Builder, t comment.Text) {
	switch v := t.(type) {
	case comment.Plain:
		b.WriteString(string(v))
	case comment.Italic:
		b.WriteString(string(v))
	case *comment.Link:
		for _, x := range v.Text {
			plainText(b, x)
		}
	case *comment.DocLink:
		for _, x := range v.Text {
			plainText(b, x)
		}
	}
}

// render formats the generated file for package pkg.
func render(pkg string, methods []method) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("// Code generated by gonf-desc from the task methods' doc comments. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n", pkg)
	for _, m := range methods {
		fmt.Fprintf(&b, "\nfunc (%s) Desc%s() string { return %s }\n", m.recv, m.name, strconv.Quote(m.desc))
	}
	return format.Source(b.Bytes())
}
