package file

import (
	"errors"
	"io/fs"
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

// stringReplaceRedactor is a minimal ErrorRedactor fake: it replaces every
// occurrence of secret with secret.Redacted's real stand-in text, mirroring
// what secret.Values.Redact does for a single tracked value, without this
// in-package test importing package secret or api (this package's own
// tests cannot import api; see AGENTS.md, "Test seams").
type stringReplaceRedactor struct{ secret string }

func (r stringReplaceRedactor) Redact(s string) string {
	return strings.ReplaceAll(s, r.secret, "[redacted]")
}

// TestRenderTemplateFileLeaksRawSecretWithNoRedactorInstalled pins task
// if2's confirmed leak and its documented boundary: called directly (task
// if2's exact probe — not through api.RenderTemplate) with no ErrorRedactor
// installed, RenderTemplateFile still returns the raw, unredacted
// text/template error, exactly as file.go's package doc comment and this
// function's own WARNING say it does. This is intentional for Option A
// (SetErrorRedactor is opt-in per process): the fix is
// TestRenderTemplateFileRedactsResolvedSecretWhenRedactorInstalled below,
// which installs the redactor api's init wires in production.
func TestRenderTemplateFileLeaksRawSecretWithNoRedactorInstalled(t *testing.T) {
	SetErrorRedactor(nil)
	const secretValue = "s3cr3t-Passw0rd-Value"
	path := filepath.Join(t.TempDir(), "leaky.tmpl")
	if err := os.WriteFile(path, []byte(`{{range .Password}}x{{end}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RenderTemplateFile(path, map[string]any{"Password": secretValue})
	if err == nil {
		t.Fatal("RenderTemplateFile: want an error ranging over a non-iterable string")
	}
	if !strings.Contains(err.Error(), secretValue) {
		t.Fatalf("RenderTemplateFile error = %v, want the raw secret with no redactor installed", err)
	}
}

// TestRenderTemplateFileRedactsResolvedSecretWhenRedactorInstalled
// reproduces task if2's probe but with the ErrorRedactor api's init
// installs in production (a stand-in here, since this in-package test
// cannot import api): the raw secret from the SAME direct
// resource/file.RenderTemplateFile call (bypassing api.RenderTemplate
// entirely) no longer appears in the returned error, closing the leak for
// any caller that links this package alongside api.
func TestRenderTemplateFileRedactsResolvedSecretWhenRedactorInstalled(t *testing.T) {
	const secretValue = "s3cr3t-Passw0rd-Value"
	SetErrorRedactor(stringReplaceRedactor{secret: secretValue})
	t.Cleanup(func() { SetErrorRedactor(nil) })

	path := filepath.Join(t.TempDir(), "leaky.tmpl")
	if err := os.WriteFile(path, []byte(`{{range .Password}}x{{end}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RenderTemplateFile(path, map[string]any{"Password": secretValue})
	if err == nil {
		t.Fatal("RenderTemplateFile: want an error ranging over a non-iterable string")
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("RenderTemplateFile error leaks the raw secret: %v", err)
	}
	if !strings.Contains(err.Error(), "template execute error") {
		t.Fatalf("RenderTemplateFile error lost its (non-secret) step context: %v", err)
	}
}

// TestRenderTemplateFileReadErrorUnaffectedByRedactor pins that a read
// failure (readForSource's error, carrying no template data at all) passes
// through unchanged even with a redactor installed: redactRenderError only
// replaces an error when its own Redact call actually changes the text, so
// errors.Is/errors.As on the read error keep working for a caller — the
// same identity-preserving rule task jf2 applies at the api layer.
func TestRenderTemplateFileReadErrorUnaffectedByRedactor(t *testing.T) {
	SetErrorRedactor(stringReplaceRedactor{secret: "s3cr3t-Passw0rd-Value"})
	t.Cleanup(func() { SetErrorRedactor(nil) })

	_, err := RenderTemplateFile(filepath.Join(t.TempDir(), "missing.tmpl"), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "failed to read source file") {
		t.Fatalf("RenderTemplateFile error = %v, want a read error", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("RenderTemplateFile error = %v, want errors.Is(err, fs.ErrNotExist) to still hold", err)
	}
}
