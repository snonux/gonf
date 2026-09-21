package options

import (
	"go/types"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestOptionCasesCoverEveryOption keeps optionCases exhaustive: every exported
// option constructor (a function whose single result is an option, whatever
// its declared type: an unexported closure type such as fileOption, or an
// exported family interface such as FileOption) and every exported option
// value must have a TestOptionFamilyMatrix row, so a new option cannot ship
// with an untested family set. "Option" means the type has the Apply(any)
// method every resource constructor calls.
func TestOptionCasesCoverEveryOption(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range optionCases {
		covered[tc.name] = true
	}
	found := map[string]bool{}
	var missing []string
	for _, name := range exportedOptionNames(t) {
		found[name] = true
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("option %s has no optionCases row in matrix_test.go", name)
	}
	// The reverse direction keeps the scan honest: a row the scan does not
	// find means the row is stale or the detection broke (which would
	// otherwise make this test pass vacuously).
	for _, tc := range optionCases {
		if !found[tc.name] {
			t.Errorf("optionCases row %s matches no exported option found in the package", tc.name)
		}
	}
}

// TestFamilyListsMatchTheDeclaredFamilies keeps the hand-kept family lists
// honest. A resource family is an exported interface whose method set is
// exactly Apply(any) plus one unexported marker (FileOption, ...); composite
// families such as AllResourceOption carry several markers and are excluded.
//   - allFamilies (hence Families(), and through it the real-resource check in
//     resource_capability_test.go) must list exactly the declared families,
//     so a new family (say ZoneOption) cannot slip past the matrix;
//   - famAll must list exactly the families AllResourceOption satisfies.
func TestFamilyListsMatchTheDeclaredFamilies(t *testing.T) {
	scope := packageScope(t)
	declared := declaredFamilies(scope)
	if listed := Families(); !slices.Equal(sortedCopy(listed), declared) {
		t.Errorf("allFamilies = %v, declared family interfaces = %v", sortedCopy(listed), declared)
	}

	all := scope.Lookup("AllResourceOption").Type()
	var satisfied []string
	for _, fam := range declared {
		iface := scope.Lookup(fam + "Option").Type().Underlying().(*types.Interface)
		if types.Implements(all, iface) {
			satisfied = append(satisfied, fam)
		}
	}
	if !slices.Equal(sortedCopy(famAll), satisfied) {
		t.Errorf("famAll = %v, AllResourceOption satisfies %v", sortedCopy(famAll), satisfied)
	}
}

// declaredFamilies returns the sorted family names (type name minus the
// "Option" suffix) of every single-marker family interface in scope.
func declaredFamilies(scope *types.Scope) []string {
	var names []string
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !tn.Exported() || !strings.HasSuffix(name, "Option") {
			continue
		}
		iface, ok := tn.Type().Underlying().(*types.Interface)
		if !ok || iface.NumMethods() != 2 || !isOptionType(tn.Type()) {
			continue
		}
		unexported := 0
		for i := range iface.NumMethods() {
			if !iface.Method(i).Exported() {
				unexported++
			}
		}
		if unexported == 1 {
			names = append(names, strings.TrimSuffix(name, "Option"))
		}
	}
	return names
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// packageScope type-checks this package's non-test sources (exported API
// only, from export data) and returns its package scope. GOPACKAGESDRIVER is
// forced off so a user-configured driver (e.g. Bazel) cannot change what is
// loaded.
func packageScope(t *testing.T) *types.Scope {
	t.Helper()
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedTypes | packages.NeedName,
		Env:  append(os.Environ(), "GOPACKAGESDRIVER=off"),
	}, ".")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("load resource/options: got %d packages, want 1", len(loaded))
	}
	if len(loaded[0].Errors) != 0 {
		t.Fatalf("load resource/options: %v", loaded[0].Errors)
	}
	return loaded[0].Types.Scope()
}

// exportedOptionNames type-checks this package's non-test sources and returns
// the exported functions and variables that produce an option.
func exportedOptionNames(t *testing.T) []string {
	t.Helper()
	scope := packageScope(t)
	var names []string
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj.Exported() && producesOption(obj) {
			names = append(names, name)
		}
	}
	return names
}

// producesOption reports whether obj is a receiver-less function with a
// single option result, or a variable of option type.
func producesOption(obj types.Object) bool {
	switch o := obj.(type) {
	case *types.Func:
		sig := o.Type().(*types.Signature)
		return sig.Recv() == nil && sig.Results().Len() == 1 && isOptionType(sig.Results().At(0).Type())
	case *types.Var:
		return isOptionType(o.Type())
	}
	return false
}

// isOptionType reports whether t has the method set of a resource option:
// Apply(any). The erased Option func type and GuardOption have no methods and
// are deliberately excluded (they are not applied by resource constructors).
func isOptionType(t types.Type) bool {
	obj, _, _ := types.LookupFieldOrMethod(t, true, nil, "Apply")
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	params := fn.Type().(*types.Signature).Params()
	return params.Len() == 1 && types.Identical(params.At(0).Type(), types.Universe.Lookup("any").Type())
}
