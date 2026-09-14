package plan

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeDecodePushNoBlobs(t *testing.T) {
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/x", ContentB64: "eA=="},
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, nil); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ops) != 2 || got.Ops[0].ID != "p" || got.PlanDir != "" {
		t.Fatalf("%#v", got)
	}
}

func TestEncodeDecodePushWithMemoryBlobs(t *testing.T) {
	mem := NewMemoryStore()
	ref, err := mem.WriteFile("secret", []byte("sekrit\n"))
	if err != nil {
		t.Fatal(err)
	}
	ops := []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "p"},
		{Op: KindFile, Path: "/tmp/secret", Blob: ref},
	}
	var buf bytes.Buffer
	if err := EncodePush(&buf, ops, mem); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(&buf, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanDir != dir {
		t.Fatalf("planDir=%q", got.PlanDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sekrit\n" {
		t.Fatalf("blob data %q", data)
	}
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(ref)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %#o", info.Mode().Perm())
	}
}

func TestDecodePushBareJSONL(t *testing.T) {
	raw, err := EncodePlan([]Op{{Op: KindPlan, Version: CurrentVersion, ID: "bare"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePush(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ops[0].ID != "bare" {
		t.Fatalf("%#v", got.Ops)
	}
}

func TestDecodePushRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	err := extractTarHeader(dir, &tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Size: 0}, bytes.NewReader(nil))
	if err == nil || !strings.Contains(err.Error(), "zip-slip") {
		t.Fatalf("want zip-slip error, got %v", err)
	}
}

func TestDecodePushBadMagic(t *testing.T) {
	_, err := DecodePush(strings.NewReader("NOPE\nblobs 0\nplan\n"), "")
	if err == nil || !strings.Contains(err.Error(), "bad magic") {
		t.Fatalf("want bad magic, got %v", err)
	}
}
