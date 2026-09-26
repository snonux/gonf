// Package gendesc generates DescX task-description companions from the doc
// comments of a recipe package's task methods (cmd/gonf-desc).
package gendesc

import (
	"bytes"
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

// Generate returns the formatted source of the generated file for the
// package in dir, or nil when no task method there needs a description.
// out, the generated file's name, is skipped when reading the package.
func Generate(dir, out string) ([]byte, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == out {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		if !buildable(f) {
			continue
		}
		files = append(files, f)
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
	return render(files[0].Name.Name, methods)
}

// buildable leaves out files that exclude themselves from every build
// (//go:build ignore), such as other generators.
func buildable(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if c.Text == "//go:build ignore" {
				return false
			}
		}
	}
	return true
}

// collect returns the DescX companions to generate, sorted by receiver and
// method name. A method defined in several build-constrained files (e.g.
// s_linux.go and s_freebsd.go) gets one companion, since desc_gen.go has no
// build constraint; differing doc comments are an error, as either
// description would be wrong on the other platform.
func collect(files []*ast.File) ([]method, error) {
	type key struct{ recv, name string }
	have := map[key]bool{}
	var candidates []method
	for _, f := range files {
		for _, d := range f.Decls {
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
