package options

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"
)

// resourceOptionsDir is the source directory of the package this package
// re-exports, relative to this test's working directory (api/options).
const resourceOptionsDir = "../../resource/options"

// aliasAllowlist names resource/options exports that intentionally have NO
// api/options counterpart. Each entry must carry the reason it is excluded;
// TestAliasSurfaceIsExhaustive fails when an entry goes stale (the export was
// removed, or an alias was added after all).
var aliasAllowlist = map[string]string{
	// NormalizeMode is the validator WithMode/WithFileMode apply internally.
	// It aborts the process (logger.Fatal) on out-of-range bits, so the
	// recipe DSL exposes it only indirectly through those options; the
	// compatibility surface keeps just an unexported test wrapper (mode.go).
	"NormalizeMode": "internal WithMode validator; exits on bad input",
}

// TestAliasSurfaceIsExhaustive is the fitness test for the hand-maintained
// re-export list in option.go (and its siblings): every exported top-level
// identifier of resource/options must have a same-named api/options export,
// unless aliasAllowlist names it with a reason. Without it, a new option added
// to resource/options silently stays missing from the older import path.
//
// It parses the build-selected (non-test) sources with go/ast instead of
// using reflection, because Go cannot enumerate a package's declarations at
// run time.
func TestAliasSurfaceIsExhaustive(t *testing.T) {
	source := exportedNames(t, resourceOptionsDir)
	aliases := exportedNames(t, ".")

	var missing []string
	for name := range source {
		if _, ok := aliases[name]; ok {
			continue
		}
		if _, ok := aliasAllowlist[name]; ok {
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("resource/options.%s has no api/options alias; add it to api/options (or allowlist it with a reason)", name)
	}

	for name := range aliasAllowlist {
		if _, ok := source[name]; !ok {
			t.Errorf("aliasAllowlist entry %q is stale: resource/options no longer exports it", name)
		}
		if _, ok := aliases[name]; ok {
			t.Errorf("aliasAllowlist entry %q is stale: api/options now exports it", name)
		}
	}
}

// parsePackage parses the non-test Go files go/build selects for dir under
// the current build context, so build-tagged files are handled like the
// compiler handles them.
func parsePackage(t *testing.T, dir string) []*ast.File {
	t.Helper()
	pkg, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("import %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(pkg.GoFiles))
	for _, name := range pkg.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	return files
}

// exportedNames returns the exported top-level identifiers (functions without
// a receiver, types, vars and consts) declared in dir.
func exportedNames(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	names := map[string]struct{}{}
	add := func(id *ast.Ident) {
		if id.IsExported() {
			names[id.Name] = struct{}{}
		}
	}
	for _, f := range parsePackage(t, dir) {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					add(d.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						add(s.Name)
					case *ast.ValueSpec:
						for _, id := range s.Names {
							add(id)
						}
					}
				}
			}
		}
	}
	return names
}
