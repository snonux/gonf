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

// TestRenderTemplateRedactsResolvedSecretFromExecuteError reproduces task
// 1f2's probe: a template ({{range .Password}}x{{end}}) executed against
// data holding a resolved secret fails inside text/template (range cannot
// iterate over a plain string) with an error that would otherwise quote the
// secret's exact bytes verbatim. Unlike a destination render
// (resource/file/template.go's templateError), RenderTemplate has no File
// resource and so no WithSensitive flag to gate on; the fix instead redacts
// every value ResolveSecret/MustSecret has ever returned (RedactSecrets),
// which applies identically regardless of any WithSensitive a caller might
// or might not attach to a later, unrelated File resource — there is
// nothing here for such a flag to change.
func TestRenderTemplateRedactsResolvedSecretFromExecuteError(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	useSecretWorkDir(t)
	const secretValue = "s3cr3t-Passw0rd-Value"
	writeSecret(t, "svc/password", secretValue)
	pw := MustSecret("svc/password")
	if pw != secretValue {
		t.Fatalf("MustSecret = %q, want %q", pw, secretValue)
	}

	path := filepath.Join(t.TempDir(), "leaky.tmpl")
	if err := os.WriteFile(path, []byte(`{{range .Password}}x{{end}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := RenderTemplate(path, map[string]any{"Password": pw})
	if err == nil {
		t.Fatal("RenderTemplate: want an error ranging over a non-iterable string")
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("RenderTemplate error leaks the raw secret: %v", err)
	}
	if !strings.Contains(err.Error(), "template execute error") {
		t.Fatalf("RenderTemplate error lost its (non-secret) step context: %v", err)
	}
}

// TestRenderTemplateNonSecretErrorStaysUseful pins the other side of the
// same fix: RedactSecrets only removes bytes it recognises as a resolved
// secret, so a template error that never touched one keeps its full,
// genuinely useful detail — task 1f2 deliberately rejected a blanket
// "always withhold" design for RenderTemplate for this reason.
func TestRenderTemplateNonSecretErrorStaysUseful(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typo.tmpl")
	if err := os.WriteFile(path, []byte(`{{.Typo}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RenderTemplate(path, map[string]any{"Name": "value"})
	if err == nil || !strings.Contains(err.Error(), `map has no entry for key "Typo"`) {
		t.Fatalf("RenderTemplate error = %v, want the full missing-key detail preserved", err)
	}
}
