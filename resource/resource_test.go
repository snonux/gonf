package resource

import (
	"testing"
)

// noopApplier is the registered value of resources these tests only
// register; nothing applies them.
var noopApplier = ApplierFunc(func() error { return nil })

func TestResourceID(t *testing.T) {
	ResetRepository()
	res := Register("File", "/tmp/foo.txt", noopApplier)
	expected := "File[/tmp/foo.txt]"
	if res.ID() != expected {
		t.Errorf("expected ID %s, got %s", expected, res.ID())
	}
}

func TestResourceString(t *testing.T) {
	ResetRepository()
	res := Register("File", "/tmp/foo.txt", noopApplier)
	expected := "File[/tmp/foo.txt]"
	if res.String() != expected {
		t.Errorf("expected String %s, got %s", expected, res.String())
	}
}

func TestNew(t *testing.T) {
	ResetRepository()
	type_ := "File"
	name := "/tmp/foo.txt"
	res := Register(type_, name, noopApplier)

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
	ResetRepository()
	repo := getRepository()
	res := Register("File", "/tmp/foo.txt", noopApplier)

	// First registration already happened in New()
	// But we can try to register again via the repository directly
	if err := repo.register(res); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}

	// Registration of a different resource should succeed
	res2 := Register("File", "/tmp/bar.txt", noopApplier)
	if err := repo.register(res2); err == nil {
		t.Error("expected error when registering the same resource twice, got nil")
	}
}
