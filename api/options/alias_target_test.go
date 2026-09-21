package options

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// resourceOptionsPath is the import path every alias must point into.
const resourceOptionsPath = "github.com/snonux/gonf/resource/options"

// minCheckedAliases is a floor well below the ~155 current aliases. It exists
// only so a syntax or layout change that hides every declaration from the
// checks cannot make TestAliasesPointAtSameNamedSource pass vacuously.
const minCheckedAliases = 140

// TestAliasesPointAtSameNamedSource guards the other half of the alias
// contract (TestAliasSurfaceIsExhaustive checks presence). Every exported
// type, var and const in api/options must be an alias of the resource/options
// identifier of the SAME name:
//   - a type needs the `=`: without it (`GuardOption resourceoptions.GuardOption`)
//     it becomes a new, incompatible defined type that still compiles;
//   - the selector's qualifier must be the resource/options import, not
//     another package that happens to export the same name;
//   - the name must match, so `WithHour = resourceoptions.WithMinute` fails.
//
// Wrapper functions (mode.go) are FuncDecls and are not checked here.
func TestAliasesPointAtSameNamedSource(t *testing.T) {
	checked := 0
	for _, f := range parsePackage(t, ".") {
		qualifier := importName(f, resourceOptionsPath)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				for _, problem := range aliasProblems(spec, qualifier) {
					t.Error(problem)
				}
				checked += exportedSpecNames(spec)
			}
		}
	}
	if checked < minCheckedAliases {
		t.Errorf("checked only %d exported aliases, want at least %d; did the alias layout change?", checked, minCheckedAliases)
	}
}

// TestAliasProblemsRejectsMutations pins the checker itself on parsed (never
// compiled) snippets, so mutations that would not even build here, such as an
// alias into a foreign package, are still proven to be caught.
func TestAliasProblemsRejectsMutations(t *testing.T) {
	tests := []struct {
		name, src   string
		wantProblem bool
	}{
		{"type alias", "type GuardOption = ro.GuardOption", false},
		{"var alias", "var WithHour = ro.WithHour", false},
		{"const alias", "const CandidatePath = ro.CandidatePath", false},
		{"unexported ignored", "var helper = other.Thing", false},
		{"defined type without =", "type GuardOption ro.GuardOption", true},
		{"foreign qualifier", "var WithHour = other.WithHour", true},
		{"wrong name", "var WithHour = ro.WithMinute", true},
		{"not a selector", "var WithHour = func() {}", true},
		{"missing value", "var WithHour func()", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\nimport ro " + strconv.Quote(resourceOptionsPath) + "\n" + tt.src + "\n"
			f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			spec := f.Decls[len(f.Decls)-1].(*ast.GenDecl).Specs[0]
			problems := aliasProblems(spec, importName(f, resourceOptionsPath))
			if got := len(problems) > 0; got != tt.wantProblem {
				t.Errorf("problems = %v, want problem: %v", problems, tt.wantProblem)
			}
		})
	}
}

// importName returns the local name under which file f imports path, or ""
// when f does not import it (every selector in f is then rejected).
func importName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return filepath.Base(path)
	}
	return ""
}

// exportedSpecNames counts the exported names a type or value spec declares.
func exportedSpecNames(spec ast.Spec) int {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Name.IsExported() {
			return 1
		}
	case *ast.ValueSpec:
		n := 0
		for _, id := range s.Names {
			if id.IsExported() {
				n++
			}
		}
		return n
	}
	return 0
}

// aliasProblems returns one message per exported name in spec that is not a
// true alias of the same-named identifier behind qualifier.
func aliasProblems(spec ast.Spec, qualifier string) []string {
	var problems []string
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if !s.Name.IsExported() {
			break
		}
		if !s.Assign.IsValid() {
			problems = append(problems, fmt.Sprintf("api/options.%s is a new defined type; write `%s = %s.%s` to alias it", s.Name.Name, s.Name.Name, qualifier, s.Name.Name))
		} else if msg := selectorProblem(s.Name.Name, s.Type, qualifier); msg != "" {
			problems = append(problems, msg)
		}
	case *ast.ValueSpec:
		for i, id := range s.Names {
			if !id.IsExported() {
				continue
			}
			var value ast.Expr
			if i < len(s.Values) {
				value = s.Values[i]
			}
			if msg := selectorProblem(id.Name, value, qualifier); msg != "" {
				problems = append(problems, msg)
			}
		}
	}
	return problems
}

// selectorProblem checks that expr is exactly `qualifier.name` and returns a
// description of the mismatch, or "" when it is.
func selectorProblem(name string, expr ast.Expr, qualifier string) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return fmt.Sprintf("api/options.%s is not a plain alias of %s.%s", name, resourceOptionsPath, name)
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || qualifier == "" || pkg.Name != qualifier {
		return fmt.Sprintf("api/options.%s aliases a selector outside %s (want %s.%s)", name, resourceOptionsPath, qualifier, name)
	}
	if sel.Sel.Name != name {
		return fmt.Sprintf("api/options.%s aliases %s.%s; want %s.%s", name, qualifier, sel.Sel.Name, qualifier, name)
	}
	return ""
}
