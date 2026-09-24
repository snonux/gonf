package systemd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestNoPackageLevelClientBypass pins task pg2: no package-level function
// in this package's production code builds a zero Client{} (except
// NewClient, whose nil-runners default IS the real runner by contract).
// The removed IsActive/IsEnabled/Run shortcuts did exactly that, so a
// call site using them silently ignored the per-apply ctx.Runners.Systemd
// injection and, in a test, reached the host's real systemctl. Methods on
// Client are exempt: they run through their own receiver's runner.
func TestNoPackageLevelClientBypass(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range zeroClientFuncs(f) {
			t.Errorf("%s: package-level func %s builds a zero Client{}, bypassing runner injection", name, fn)
		}
	}
}

// zeroClientFuncs returns the names of f's receiver-less functions, other
// than NewClient, whose body contains an empty Client{} composite literal.
func zeroClientFuncs(f *ast.File) []string {
	var hits []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || fn.Name.Name == "NewClient" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if ok && len(lit.Elts) == 0 && isIdent(lit.Type, "Client") {
				hits = append(hits, fn.Name.Name)
				return false
			}
			return true
		})
	}
	return hits
}

// isIdent reports whether expr is the bare identifier name.
func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}
