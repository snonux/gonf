package resource

import (
	"reflect"
	"testing"
)

func TestResourceDependencies(t *testing.T) {
	r := Resource{Type: "File", Name: "/tmp/a"}

	got := r.Dependencies()
	want := []string{"File[/tmp/a]"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resource.Dependencies() = %v, want %v", got, want)
	}
}

// TestMultiDependencies ensures a Multi flattens into each member's individual
// ID, so depending on a Multi records a dependency on every member.
func TestMultiDependencies(t *testing.T) {
	m := Multi{
		Resource{Type: "File", Name: "/tmp/a"},
		Resource{Type: "File", Name: "/tmp/b"},
	}

	got := m.Dependencies()
	want := []string{"File[/tmp/a]", "File[/tmp/b]"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Multi.Dependencies() = %v, want %v", got, want)
	}
}

// TestRegisterWithDeps ensures Register seeds dependsOn from the variadic deps
// and de-duplicates repeated IDs.
func TestRegisterWithDeps(t *testing.T) {
	ResetRepository()

	res := Register("File", "/tmp/a", &mockApplier{}, "File[b]", "File[c]", "File[b]")

	if _, ok := res.dependsOn["File[b]"]; !ok {
		t.Error("expected dependency File[b]")
	}
	if _, ok := res.dependsOn["File[c]"]; !ok {
		t.Error("expected dependency File[c]")
	}
	if len(res.dependsOn) != 2 {
		t.Errorf("expected 2 unique dependencies, got %d: %v", len(res.dependsOn), res.dependsOn)
	}
}

func TestRegisterNoDeps(t *testing.T) {
	ResetRepository()

	res := Register("File", "/tmp/a", &mockApplier{})
	if res.dependsOn == nil {
		t.Error("expected dependsOn to be initialized, got nil")
	}
	if len(res.dependsOn) != 0 {
		t.Errorf("expected no dependencies, got %v", res.dependsOn)
	}
}

// TestSortedDependsOn ensures dependency IDs are returned sorted for stable log
// output regardless of insertion/map order.
func TestSortedDependsOn(t *testing.T) {
	ResetRepository()

	res := Register("File", "/tmp/a", &mockApplier{}, "File[c]", "File[a]", "File[b]")

	got := res.sortedDependsOn()
	want := []string{"File[a]", "File[b]", "File[c]"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortedDependsOn() = %v, want %v", got, want)
	}
}
