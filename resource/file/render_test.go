package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTemplateStructuredAndFunctions(t *testing.T) {
	got, err := RenderTemplate(`{{upper .Name}} {{join .Ports ","}} {{.Data.Name}} {{trim " x "}}`, templateTestServer{
		Name:  "relay",
		Ports: []int{80, 443},
	})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if want := "RELAY 80,443 relay x"; got != want {
		t.Errorf("RenderTemplate = %q, want %q", got, want)
	}
}

func TestRenderTemplateMissingKeyFails(t *testing.T) {
	_, err := RenderTemplate(`{{.Missing}}`, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "map has no entry for key \"Missing\"") {
		t.Fatalf("RenderTemplate error = %v, want missing-key error", err)
	}
}

func TestRenderTemplateParseErrorFails(t *testing.T) {
	_, err := RenderTemplate(`{{.Name`, map[string]any{"Name": "x"})
	if err == nil || !strings.Contains(err.Error(), "template parse error") {
		t.Fatalf("RenderTemplate error = %v, want a parse error", err)
	}
}

func TestRenderTemplateDataNotJSONCompatibleFails(t *testing.T) {
	_, err := RenderTemplate(`{{.x}}`, map[string]any{"x": func() {}})
	if err == nil || !strings.Contains(err.Error(), "JSON-compatible") {
		t.Fatalf("RenderTemplate error = %v, want a JSON-compatible error", err)
	}
}

// TestRenderTemplateHasNoDestinationFacts pins the documented difference
// from a destination render: there is no {{.Gonf}} and no process
// environment here, since controller-side rendering runs before any
// destination is selected.
func TestRenderTemplateHasNoDestinationFacts(t *testing.T) {
	_, err := RenderTemplate(`{{.Gonf.GOOS}}`, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "map has no entry for key \"Gonf\"") {
		t.Fatalf("RenderTemplate error = %v, want a missing .Gonf error", err)
	}

	t.Setenv("GONF_RENDER_TEMPLATE_TEST_VAR", "leaked")
	_, err = RenderTemplate(`{{.GONF_RENDER_TEMPLATE_TEST_VAR}}`, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "map has no entry for key \"GONF_RENDER_TEMPLATE_TEST_VAR\"") {
		t.Fatalf("RenderTemplate error = %v, want a missing-env-var error", err)
	}
}

func TestRenderTemplateFileReadsAndRenders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.tmpl")
	if err := os.WriteFile(path, []byte(`hello {{.name}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := RenderTemplateFile(path, map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("RenderTemplateFile: %v", err)
	}
	if want := "hello world"; got != want {
		t.Errorf("RenderTemplateFile = %q, want %q", got, want)
	}
}

func TestRenderTemplateFileMissingPathFails(t *testing.T) {
	_, err := RenderTemplateFile(filepath.Join(t.TempDir(), "missing.tmpl"), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "failed to read source file") {
		t.Fatalf("RenderTemplateFile error = %v, want a read error", err)
	}
}
