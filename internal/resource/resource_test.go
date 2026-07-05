package resource

import (
	"testing"
)

func TestResourceID(t *testing.T) {
	resetRepository()
	res := Register("File", "/tmp/foo.txt", &mockApplier{})
	expected := "File[/tmp/foo.txt]"
	if res.ID() != expected {
		t.Errorf("expected ID %s, got %s", expected, res.ID())
	}
}

func TestResourceString(t *testing.T) {
	resetRepository()
	res := Register("File", "/tmp/foo.txt", &mockApplier{})
	expected := "File[/tmp/foo.txt]"
	if res.String() != expected {
		t.Errorf("expected String %s, got %s", expected, res.String())
	}
}

func TestNew(t *testing.T) {
	resetRepository()
	type_ := "File"
	name := "/tmp/foo.txt"
	res := Register(type_, name, &mockApplier{})

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
	resetRepository()
	repo := getRepository()
	res := Register("File", "/tmp/foo.txt", &mockApplier{})

	// First registration already happened in New()
	// But we can try to register again via the repository directly
	if err := repo.register(res); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}

	// Registration of a different resource should succeed
	res2 := Register("File", "/tmp/bar.txt", &mockApplier{})
	if err := repo.register(res2); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}
}

type mockApplier struct{}

func (m *mockApplier) Apply() error { return nil }
