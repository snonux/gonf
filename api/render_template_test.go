package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTemplateRendersFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "httpd.conf.tmpl")
	if err := os.WriteFile(path, []byte(`server "{{.FQDN}}" { listen on * port 80 }`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := RenderTemplate(path, map[string]any{"FQDN": "example.org"})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if want := `server "example.org" { listen on * port 80 }`; got != want {
		t.Errorf("RenderTemplate = %q, want %q", got, want)
	}
}

func TestRenderTemplateMissingKeyFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.tmpl")
	if err := os.WriteFile(path, []byte(`{{.Missing}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RenderTemplate(path, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "map has no entry for key \"Missing\"") {
		t.Fatalf("RenderTemplate error = %v, want missing-key error", err)
	}
}

func TestRenderTemplateMissingFileFails(t *testing.T) {
	_, err := RenderTemplate(filepath.Join(t.TempDir(), "missing.tmpl"), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "failed to read source file") {
		t.Fatalf("RenderTemplate error = %v, want a read error", err)
	}
}
