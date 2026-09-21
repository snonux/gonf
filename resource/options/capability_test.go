package options

import (
	"go/types"
	"os"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestRecorderImplementsEveryCapability type-checks this package together
// with its tests and asserts *recorder implements every capability interface
// (every exported interface without the Apply(any) method of an option
// family). recorder_test.go also lists the capabilities as compile-time
// assertions, but only this test catches a NEW capability that the list and
// the recorder both lack.
func TestRecorderImplementsEveryCapability(t *testing.T) {
	// NeedSyntax makes go/packages type-check the root package from source;
	// with NeedTypes alone it reads export data, which omits the unexported,
	// test-only recorder.
	loaded, err := packages.Load(&packages.Config{
		Mode:  packages.NeedTypes | packages.NeedName | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests: true,
		Env:   append(os.Environ(), "GOPACKAGESDRIVER=off"),
	}, ".")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	scope := testVariantScope(t, loaded)
	recorderPtr := types.NewPointer(scope.Lookup("recorder").Type())
	checked := 0
	for _, name := range scope.Names() {
		iface, ok := capabilityInterface(scope.Lookup(name))
		if !ok {
			continue
		}
		checked++
		if !types.Implements(recorderPtr, iface) {
			t.Errorf("*recorder does not implement capability %s; add the setter to recorder_test.go", name)
		}
	}
	// There are ~57 capabilities today; a floor keeps the loop from passing
	// vacuously if the classification below stops matching.
	if checked < 50 {
		t.Errorf("checked only %d capability interfaces, want at least 50", checked)
	}
}

// testVariantScope returns the scope of the loaded package variant that
// includes this package's _test.go files (the one declaring recorder).
func testVariantScope(t *testing.T, loaded []*packages.Package) *types.Scope {
	t.Helper()
	for _, pkg := range loaded {
		if pkg.Types == nil {
			continue
		}
		if scope := pkg.Types.Scope(); scope.Lookup("recorder") != nil {
			if len(pkg.Errors) != 0 {
				t.Fatalf("type-check %s: %v", pkg.ID, pkg.Errors)
			}
			return scope
		}
	}
	for _, pkg := range loaded {
		t.Logf("loaded %s (types: %v, errors: %v)", pkg.ID, pkg.Types != nil, pkg.Errors)
	}
	t.Fatal("no loaded package variant declares recorder")
	return nil
}

// capabilityInterface reports whether obj is an exported capability
// interface: an interface type that is not an option family (those carry
// Apply(any)).
func capabilityInterface(obj types.Object) (*types.Interface, bool) {
	tn, ok := obj.(*types.TypeName)
	if !ok || !tn.Exported() {
		return nil, false
	}
	iface, ok := tn.Type().Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 || isOptionType(tn.Type()) {
		return nil, false
	}
	return iface, true
}
