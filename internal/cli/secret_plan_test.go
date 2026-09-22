package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// fakeCLISecret is synthetic secret material for the plan output tests.
const fakeCLISecret = "fake-cli-token-9a8b7c6d"

// registerSecretTask writes the fake secret below a fresh secrets/ directory
// (the working directory is changed for the test) and registers task
// "cli_secret", which manages one secret-bearing and one plain file.
func registerSecretTask(t *testing.T) string {
	t.Helper()
	api.ResetForTest()
	t.Cleanup(api.ResetForTest)
	work := t.TempDir()
	t.Chdir(work)
	if err := os.MkdirAll(filepath.Join("secrets", "svc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("secrets", "svc", "token"), []byte(fakeCLISecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api.Task("cli_secret", "", func() {
		api.File(filepath.Join(work, "token"), options.WithContent(api.MustSecret("svc/token")), options.WithMode(0o600))
		api.File(filepath.Join(work, "plain"), options.WithContent("plain\n"))
	})
	return work
}

// leaksCLISecret reports whether out holds the fake secret, raw or encoded.
func leaksCLISecret(out string) bool {
	return strings.Contains(out, fakeCLISecret) ||
		strings.Contains(out, base64.StdEncoding.EncodeToString([]byte(fakeCLISecret+"\n")))
}

// -stdout refuses a secret-bearing plan without printing anything; the
// refusal names the op, not the value.
func TestCLIPlanStdoutRefusesSecretBearingPlan(t *testing.T) {
	registerSecretTask(t)
	var code int
	var stderr string
	out := captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-stdout", "cli_secret") })
	if code != 1 || out != "" || !strings.Contains(stderr, "-stdout refused: the plan carries secret material") ||
		!strings.Contains(stderr, "/token]") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want the refusal naming File[.../token]", code, out, stderr)
	}
	if leaksCLISecret(stderr) {
		t.Fatalf("refusal leaks the secret: %q", stderr)
	}
}

// -stdout -with-secrets is the explicit export: it prints the executable
// plan, secret included, which then decodes.
func TestCLIPlanStdoutWithSecretsExports(t *testing.T) {
	registerSecretTask(t)
	var code int
	out := captureStdout(t, func() { code, _ = runGonf(t, "plan", "-stdout", "-with-secrets", "cli_secret") })
	if code != 0 || !leaksCLISecret(out) {
		t.Fatalf("exit %d; the explicit export must print the plan with its secret", code)
	}
	ops, err := plan.DecodePlanBytes([]byte(out))
	if err != nil || len(plan.SensitiveIDs(ops)) != 1 {
		t.Fatalf("exported plan: %v, sensitive %v", err, plan.SensitiveIDs(ops))
	}
	if code, stderr := runGonf(t, "plan", "-with-secrets", "cli_secret"); code != 2 || !strings.Contains(stderr, "only applies to -stdout") {
		t.Fatalf("-with-secrets without -stdout: exit %d, stderr %q", code, stderr)
	}
}

// -redacted prints a preview without the secret that apply refuses.
func TestCLIPlanRedactedPreview(t *testing.T) {
	work := registerSecretTask(t)
	var code int
	var stderr string
	out := captureStdout(t, func() { code, stderr = runGonf(t, "plan", "-redacted", "cli_secret") })
	if code != 0 || leaksCLISecret(out) || leaksCLISecret(stderr) || !strings.Contains(out, "[redacted]") ||
		!strings.Contains(stderr, "1 secret-bearing; not a plan") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want a redacted preview", code, out, stderr)
	}
	preview := filepath.Join(work, "preview.jsonl")
	if err := os.WriteFile(preview, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	// cliApply (unlike CLI()'s own configureCLI) is escalate-only: its -n
	// sets resource's process-wide dry-run flag but never clears it, so it
	// survives this call. Reset it, like every other test in this package
	// that turns dry-run on, or a later test calling a subcommand handler
	// directly (bypassing CLI(), e.g. cli_cancel_test.go's
	// TestCLIApplyFileCanceledByContext) could inherit it under -shuffle=on.
	t.Cleanup(func() { resource.SetDryRun(false) })
	if code, stderr := runGonf(t, "apply", "-n", preview); code == 0 || !strings.Contains(stderr, "first op must be") {
		t.Fatalf("apply of a preview: exit %d, stderr %q; want the header refusal", code, stderr)
	}
}

// -o writes the secret-bearing plan owner-only and warns on stderr, naming
// the op but not the value.
func TestCLIPlanOutDirWarnsAboutSecretArtifact(t *testing.T) {
	work := registerSecretTask(t)
	outDir := filepath.Join(work, "out")
	code, stderr := runGonf(t, "plan", "-o", outDir, "cli_secret")
	if code != 0 || !strings.Contains(stderr, "carries secret material in clear text") || leaksCLISecret(stderr) {
		t.Fatalf("exit %d, stderr %q; want the secret-artifact warning", code, stderr)
	}
	for path, want := range map[string]os.FileMode{outDir: 0o700, filepath.Join(outDir, "plan.jsonl"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}
