package api

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests pin how loadSecret words the refusals of the descriptor walk
// below secrets/ (v62 moved that walk into internal/safepath). They call
// loadSecret directly, so the exact message is visible without the task and
// RecordPlan wrapping that secret_test.go exercises.

// wantSecretErr fails unless loadSecret(path) returns exactly want as error.
func wantSecretErr(t *testing.T, path, want string) {
	t.Helper()
	value, ok, err := loadSecret(path)
	if err == nil || err.Error() != want {
		t.Fatalf("loadSecret(%q) = (%q, %v, %v), want error %q", path, value, ok, err, want)
	}
	if value != "" || ok {
		t.Fatalf("loadSecret(%q) returned a value with an error: (%q, %v)", path, value, ok)
	}
}

// A component that is a regular file where a directory is needed has always
// been reported with the symlink wording (the walk sees ENOTDIR for both).
func TestLoadSecretRegularFileIntermediateIsReportedAsSymlink(t *testing.T) {
	useSecretWorkDir(t)
	writeSecret(t, "file", "x")
	wantSecretErr(t, "file/key", `secret path "secrets/file/key" contains a symlink`)
}

// Missing anywhere on the path, secrets/ itself included, is "missing", not an
// error: OptionalSecret relies on it to omit host fragments.
func TestLoadSecretMissingAnywhereIsNotAnError(t *testing.T) {
	useSecretWorkDir(t)
	for _, path := range []string{"key", "a/b/key"} {
		if value, ok, err := loadSecret(path); err != nil || ok || value != "" {
			t.Fatalf("loadSecret(%q) without secrets/ = (%q, %v, %v), want missing", path, value, ok, err)
		}
	}
	writeSecret(t, "a/present", "x")
	if _, ok, err := loadSecret("a/missing/key"); err != nil || ok {
		t.Fatalf("loadSecret below a missing directory = (%v, %v), want missing", ok, err)
	}
}

// Other open failures keep the bare errno after the secret's name.
func TestLoadSecretOpenFailureWording(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	useSecretWorkDir(t)
	writeSecret(t, "locked/key", "x")
	locked := filepath.Join("secrets", "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	wantSecretErr(t, "locked/key", `open secret "locked/key": permission denied`)
}

// The last component is opened without following a symlink even when the
// link points at a regular file inside secrets/, and a directory there is not
// a secret.
func TestLoadSecretFinalComponentRules(t *testing.T) {
	useSecretWorkDir(t)
	writeSecret(t, "dir/key", "x")
	if err := os.Symlink("key", filepath.Join("secrets", "dir", "alias")); err != nil {
		t.Fatal(err)
	}
	wantSecretErr(t, "dir/alias", `secret path "secrets/dir/alias" contains a symlink`)
	wantSecretErr(t, "dir", `secret "dir" is not a regular file`)
	if value, ok, err := loadSecret("/dir/key"); err != nil || !ok || value != "x" {
		t.Fatalf("loadSecret(/dir/key) = (%q, %v, %v), want the secret", value, ok, err)
	}
}
