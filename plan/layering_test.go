package plan

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/snonux/gonf"

// allowedPlanDeps lists the only non-internal packages of this module that
// plan may depend on, directly or transitively: the kind-neutral resource
// core and resource/options. Every resource/<kind> backend imports plan to
// register its Handler, so plan depending on any of them would be an import
// cycle (and would undo the Handler registry's decoupling). See Handler in
// handler.go.
var allowedPlanDeps = []string{"resource", "resource/options"}

// TestPlanImportsOnlyResourceCore pins the plan/resource layering the
// comments in handler.go, apply.go and resource/draft.go describe: walking
// plan's non-test imports transitively within this module must reach only
// internal/* packages, resource and resource/options. The compiler would
// already reject a direct cycle, but this also catches a new kind-neutral
// dependency creeping in (e.g. resource/embed), which the comments would
// then silently misdescribe. Test files are skipped because the external
// plan_test package legitimately imports resource/<kind> backends.
func TestPlanImportsOnlyResourceCore(t *testing.T) {
	seen := map[string]bool{}
	queue := []string{"plan"}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		for _, dep := range moduleImports(t, rel) {
			if !strings.HasPrefix(dep, "internal/") && !slices.Contains(allowedPlanDeps, dep) {
				t.Errorf("%s imports %s/%s: plan may only depend on internal/*, resource and resource/options", rel, modulePath, dep)
			}
			queue = append(queue, dep)
		}
	}
}

// moduleImports returns the module-relative paths of this module's packages
// imported by the non-test Go files of the package at rel (relative to the
// module root, which is the parent of this package's directory). All build
// variants are parsed, so a GOOS-specific import is not missed.
func moduleImports(t *testing.T, rel string) []string {
	t.Helper()
	dir := filepath.Join("..", filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var deps []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: bad import %s: %v", name, imp.Path.Value, err)
			}
			if dep, ok := strings.CutPrefix(p, modulePath+"/"); ok {
				deps = append(deps, dep)
			}
		}
	}
	return deps
}
