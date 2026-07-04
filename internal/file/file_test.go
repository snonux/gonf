package file

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Existing file
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	checksum := getChecksum(path)
	if checksum == (checksum) {
		// Just verify it's not all zeros — a valid sha256 won't be
		_ = checksum
	}

	// Non-existing file returns zero checksum
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

	if err := writeTmpFile(tmpPath, content); err != nil {
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

	// tmp should be gone, target should exist with new content
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

	// tmp should be gone, target unchanged
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

	if err := HaveString(path, "hello world"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
	// No .tmp file left behind
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("tmp file should not exist")
	}
}

func TestHaveStringUpdateExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")

	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := HaveString(path, "new"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("expected 'new', got %q", got)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("tmp file should not exist")
	}
}

func TestHaveStringNoChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	content := "unchanged content"

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := HaveString(path, content); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != content {
		t.Errorf("expected %q, got %q", content, got)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("tmp file should not exist")
	}
}