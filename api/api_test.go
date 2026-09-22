package api

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

// TestNoFileDoesNotMutateCallerOptionSlice is a regression test for 100 Go
// Mistakes #25: the No* constructors used to append options.IsAbsent onto
// the caller-owned variadic slice, writing IsAbsent into the spare capacity
// of a reusable option list. A later Present call built from the same
// backing array silently turned into a deletion.
func TestNoFileDoesNotMutateCallerOptionSlice(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.txt")
	keep := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(gone, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("stay"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reusable option list with spare capacity (len 2, cap 8). Sub-slices
	// share its backing array.
	base := make([]options.FileOption, 2, 8)
	base[0] = options.WithContent("stay")
	base[1] = options.WithMode(0o644)
	before := make([]options.FileOption, len(base))
	copy(before, base)

	NoFile(gone, base[:1]...)
	File(keep, base[:2]...)

	// (a) The caller-owned backing array must be unchanged: NoFile must not
	// append IsAbsent into its spare capacity.
	for i := range base {
		if reflect.ValueOf(base[i]).Pointer() != reflect.ValueOf(before[i]).Pointer() {
			t.Fatalf("caller option slice mutated at index %d: IsAbsent was injected into the caller's backing array", i)
		}
	}

	// (b) The Present resource built from the same backing array must not
	// have become absent.
	if err := Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	data, err := os.ReadFile(keep)
	if err != nil {
		t.Fatalf("expected %s to still exist: %v", keep, err)
	}
	if string(data) != "stay" {
		t.Errorf("keep.txt content = %q, want %q", data, "stay")
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", gone)
	}
}

func TestApplyUsesPlanEngineForRegisteredResources(t *testing.T) {
	ResetTasks()
	resource.ResetForTest()
	dir := t.TempDir()
	dependencyPath := filepath.Join(dir, "z-dependency")

	dependency := File(dependencyPath, options.WithContent("ready"))
	Command("test", []string{"-f", dependencyPath}, options.DependsOn(dependency))

	if err := Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	content, err := os.ReadFile(dependencyPath)
	if err != nil {
		t.Fatalf("dependency was not applied: %v", err)
	}
	if string(content) != "ready" {
		t.Fatalf("dependency content = %q, want ready", content)
	}
}

func TestApplyRejectsUndraftedRegisteredResource(t *testing.T) {
	ResetTasks()
	resource.ResetForTest()
	resource.Register("Custom", "undrafted", nil)

	err := Apply()
	if err == nil || !strings.Contains(err.Error(), "without plan drafts") {
		t.Fatalf("Apply() error = %v, want an undrafted-resource error", err)
	}
}

func TestApplyPackagesSourceThroughPlanEngine(t *testing.T) {
	ResetTasks()
	resource.ResetForTest()
	src := filepath.Join(t.TempDir(), "source.txt")
	dst := filepath.Join(t.TempDir(), "destination.txt")
	if err := os.WriteFile(src, []byte("from source"), 0o600); err != nil {
		t.Fatal(err)
	}

	InstallFile(dst, src)
	if err := Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("destination was not created: %v", err)
	}
	if string(content) != "from source" {
		t.Fatalf("destination content = %q, want source content", content)
	}
}
