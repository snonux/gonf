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

	if err := Have(path, WithContent("hello world")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

	if err := Have(path, WithContent("mode test"), WithMode(mode)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Mask to check only permission bits
	if info.Mode().Perm() != mode {
		t.Errorf("expected mode %v, got %v", mode, info.Mode().Perm())
	}
}

func TestHaveSourceFile(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join("..", "..", "assets", "testfiles", "test.txt")
	targetPath := filepath.Join(dir, "target.txt")

	if err := Have(targetPath, WithSource(sourcePath)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
	sourcePath := filepath.Join("..", "..", "assets", "testfiles", "test.tmpl")
	targetPath := filepath.Join(dir, "target.conf")

	if err := Have(targetPath, WithSource(sourcePath)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	expectedParam := sourcePath
	if !strings.Contains(string(got), expectedParam) {
		t.Errorf("expected content to contain Param %q, got %q", expectedParam, string(got))
	}
}
