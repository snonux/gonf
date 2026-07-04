package resource

import (
	"testing"
)

func TestResourceID(t *testing.T) {
	res := New("File", "/tmp/foo.txt")
	expected := "File[/tmp/foo.txt]"
	if res.ID() != expected {
		t.Errorf("expected ID %s, got %s", expected, res.ID())
	}
}

func TestResourceString(t *testing.T) {
	res := New("File", "/tmp/foo.txt")
	expected := "File[/tmp/foo.txt]"
	if res.String() != expected {
		t.Errorf("expected String %s, got %s", expected, res.String())
	}
}

func TestNew(t *testing.T) {
	type_ := "File"
	name := "/tmp/foo.txt"
	res := New(type_, name)

	if res.Type != type_ {
		t.Errorf("expected type %s, got %s", type_, res.Type)
	}
	if res.Name != name {
		t.Errorf("expected name %s, got %s", name, res.Name)
	}
	if res.dependsOn == nil {
		t.Error("expected dependsOn map to be initialized, got nil")
	}
}

func TestRepositoryRegister(t *testing.T) {
	repo := newRepository()
	res := New("File", "/tmp/foo.txt")

	// First registration should succeed
	if err := repo.register(res); err != nil {
		t.Fatalf("expected successful registration, got error: %v", err)
	}

	// Second registration of the same resource should fail
	if err := repo.register(res); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}

	// Registration of a different resource should succeed
	res2 := New("File", "/tmp/bar.txt")
	if err := repo.register(res2); err != nil {
		t.Fatalf("expected successful registration of different resource, got error: %v", err)
	}
}
