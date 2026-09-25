package api

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"
)

// TestOptionsSurfaceMatchesAPIOptions keeps the re-export list in
// options_reexport.go in step with api/options (which is itself checked
// against resource/options): every api/options export must also be
// exported by api, so a recipe never needs the second dot import.
func TestOptionsSurfaceMatchesAPIOptions(t *testing.T) {
	apiNames := topLevelExports(t, ".")
	var missing []string
	for name := range topLevelExports(t, "options") {
		if _, ok := apiNames[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("api/options.%s is not re-exported by api; add it to options_reexport.go", name)
	}
}

// topLevelExports returns the exported top-level identifiers (receiverless
// functions, types, vars and consts) of the non-test files in dir.
func topLevelExports(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	pkg, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("import %s: %v", dir, err)
	}
	names := map[string]struct{}{}
	fset := token.NewFileSet()
	for _, file := range pkg.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(dir, file), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					names[d.Name.Name] = struct{}{}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							names[s.Name.Name] = struct{}{}
						}
					case *ast.ValueSpec:
						for _, id := range s.Names {
							if id.IsExported() {
								names[id.Name] = struct{}{}
							}
						}
					}
				}
			}
		}
	}
	return names
}

// TestInventorySurfaceIsReexported keeps inventory_reexport.go complete:
// every export of package inventory must also be exported by api.
func TestInventorySurfaceIsReexported(t *testing.T) {
	apiNames := topLevelExports(t, ".")
	var missing []string
	for name := range topLevelExports(t, "../inventory") {
		if _, ok := apiNames[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("inventory.%s is not re-exported by api; add it to inventory_reexport.go", name)
	}
}
