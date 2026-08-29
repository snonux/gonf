package options

import (
	"reflect"
	"testing"

	"codeberg.org/snonux/gonf/resource"
)

// fakeTarget implements the Dependable capability so the DependsOn option can
// be tested in isolation from the concrete resource packages.
type fakeTarget struct {
	deps []string
}

func (f *fakeTarget) AddDependency(id string) { f.deps = append(f.deps, id) }

func TestDependsOnSingle(t *testing.T) {
	target := &fakeTarget{}

	dep := resource.Resource{Type: "File", Name: "a"} // ID: File[a]
	DependsOn(dep)(target)

	want := []string{"File[a]"}
	if !reflect.DeepEqual(target.deps, want) {
		t.Errorf("deps = %v, want %v", target.deps, want)
	}
}

// TestDependsOnMultiExpands ensures a Multi dependency is expanded so each of
// its members is recorded individually.
func TestDependsOnMultiExpands(t *testing.T) {
	target := &fakeTarget{}

	multi := resource.Multi{
		resource.Resource{Type: "File", Name: "a"},
		resource.Resource{Type: "File", Name: "b"},
	}
	DependsOn(multi)(target)

	want := []string{"File[a]", "File[b]"}
	if !reflect.DeepEqual(target.deps, want) {
		t.Errorf("deps = %v, want %v", target.deps, want)
	}
}

// TestDependsOnMixed ensures multiple arguments (single + multi) are all
// recorded, preserving order.
func TestDependsOnMixed(t *testing.T) {
	target := &fakeTarget{}

	single := resource.Resource{Type: "File", Name: "a"}
	multi := resource.Multi{
		resource.Resource{Type: "File", Name: "b"},
		resource.Resource{Type: "File", Name: "c"},
	}
	DependsOn(single, multi)(target)

	want := []string{"File[a]", "File[b]", "File[c]"}
	if !reflect.DeepEqual(target.deps, want) {
		t.Errorf("deps = %v, want %v", target.deps, want)
	}
}
