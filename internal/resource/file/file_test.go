package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	checksum := getChecksum(path)
	if checksum == [32]byte{} {
		t.Error("expected non-zero checksum")
	}

	zeroChecksum := getChecksum(filepath.Join(dir, "no-such-file"))
	var expectedZero [32]byte
	if zeroChecksum != expectedZero {
		t.Errorf("expected zero checksum for missing file, got %x", zeroChecksum)
	}
}

func TestWriteTmpFile(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "test.tmp")
	content := []byte("temp content")

	if err := writeTmpFile(tmpPath, content, 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("reading tmp file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("expected %q, got %q", content, got)
	}
}

func TestUpdateFromTmpChecksumChanged(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "test.tmp")
	path := filepath.Join(dir, "test.txt")

	if err := os.WriteFile(tmpPath, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := updateFromTmp(tmpPath, path, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(tmpPath); err == nil {
		t.Error("tmp file should have been removed")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading target file: %v", err)
	}
	if string(got) != "new content" {
		t.Errorf("expected 'new content', got %q", got)
	}
}

func TestUpdateFromTmpChecksumUnchanged(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "test.tmp")
	path := filepath.Join(dir, "test.txt")

	if err := os.WriteFile(tmpPath, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := updateFromTmp(tmpPath, path, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(tmpPath); err == nil {
		t.Error("tmp file should have been removed")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading target file: %v", err)
	}
	if string(got) != "same" {
		t.Errorf("expected 'same', got %q", got)
	}
}

func TestHaveStringCreateNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	Have(path, WithContent("hello world"))

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestHaveMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mode.txt")
	mode := os.FileMode(0o600)

	Have(path, WithContent("mode test"), WithMode(mode))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("expected mode %v, got %v", mode, info.Mode().Perm())
	}
}

func TestHaveSourceFile(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join("..", "..", "..", "assets", "testfiles", "test.txt")
	targetPath := filepath.Join(dir, "target.txt")

	Have(targetPath, WithSource(sourcePath))

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	expected, _ := os.ReadFile(sourcePath)
	if string(got) != string(expected) {
		t.Errorf("expected %q, got %q", string(expected), string(got))
	}
}

func TestHaveTemplateFile(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join("..", "..", "..", "assets", "testfiles", "test.tmpl")
	targetPath := filepath.Join(dir, "target.conf")

	Have(targetPath, WithSource(sourcePath))

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	expected := "Hello, " + sourcePath + "!\nWelcome to " + os.Getenv("USER") + ".\n"
	if string(got) != expected {
		t.Errorf("expected %q, got %q", expected, string(got))
	}
}

func TestHaveAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	Have(path, IsAbsent())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", path)
	}

	// Idempotent: removing a missing file is not an error (direct call to
	// avoid duplicate registration in the one-per-process registry).
	if err := ensureAbsent(path); err != nil {
		t.Fatalf("absent on missing file: %v", err)
	}
}

func TestAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	Absent(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed", path)
	}

	// Idempotent: removing a missing file is not an error (direct call to
	// avoid duplicate registration in the one-per-process registry).
	if err := ensureAbsent(path); err != nil {
		t.Fatalf("absent on missing file: %v", err)
	}
}

func TestResolveStripsTmplSuffixWhenSourceHasTmplSuffix(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "foo.conf.tmpl")
	if err := os.WriteFile(sourcePath, []byte("hello {{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Mirrors what dir's copySourceTree mechanically passes: a target path
	// that still carries the source's own ".tmpl" suffix.
	dstDir := filepath.Join(dir, "dst")
	if err := os.Mkdir(dstDir, 0o750); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(dstDir, "foo.conf.tmpl")

	if err := Ensure(targetPath, WithSource(sourcePath)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deSuffixed := filepath.Join(filepath.Dir(targetPath), "foo.conf")
	if _, err := os.Stat(deSuffixed); err != nil {
		t.Errorf("expected de-suffixed file to exist at %s: %v", deSuffixed, err)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Errorf("expected %s to NOT exist on disk", targetPath)
	}
}

func TestResolveDoesNotStripTmplWhenOnlyContentTriggersTemplate(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "foo.conf.tmpl")

	if err := Ensure(targetPath, WithContent("hello {{.Param}}")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(targetPath); err != nil {
		t.Errorf("expected %s to exist (not stripped), got %v", targetPath, err)
	}
}

func TestParamIsBareSourcePathNoPrefix(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "src.tmpl")
	if err := os.WriteFile(sourcePath, []byte("{{.Param}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(dir, "dst.conf")

	if err := Ensure(targetPath, WithSource(sourcePath)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != sourcePath {
		t.Errorf("expected Param to equal bare source path %q, got %q", sourcePath, string(got))
	}
	if strings.Contains(string(got), "source://") {
		t.Errorf("Param unexpectedly contains the removed \"source://\" prefix: %q", string(got))
	}
}
