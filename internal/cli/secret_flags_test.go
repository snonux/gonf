package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
)

// -redacted is an output of its own: combining it with -stdout,
// -with-secrets or an explicit -o is refused (exit 2) instead of silently
// picking one; -with-secrets needs -stdout.
func TestCLIPlanRejectsConflictingOutputFlags(t *testing.T) {
	work := registerSecretTask(t)
	cases := [][]string{
		{"plan", "-redacted", "-stdout", "cli_secret"},
		{"plan", "-redacted", "-with-secrets", "-stdout", "cli_secret"},
		{"plan", "-redacted", "-o", filepath.Join(work, "out"), "cli_secret"},
		{"plan", "-with-secrets", "cli_secret"},
	}
	for _, args := range cases {
		var code int
		var stderr string
		out := captureStdout(t, func() { code, stderr = runGonf(t, args...) })
		if code != 2 || out != "" || !strings.HasPrefix(stderr, "plan: -") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want a usage refusal", args, code, out, stderr)
		}
	}
}

// An unnamed Command carrying a secret argument has the secret in its ID;
// every plan output refuses it at record time without printing the secret.
func TestCLIPlanRefusesSecretInCommandIdentity(t *testing.T) {
	registerSecretTask(t)
	api.Task("cli_curl", "", func() {
		api.Command("/usr/bin/curl", api.List("-H", "Authorization: "+strings.TrimSpace(api.MustSecret("svc/token"))))
	})
	for _, args := range [][]string{
		{"plan", "-stdout", "cli_curl"},
		{"plan", "-redacted", "cli_curl"},
		{"plan", "-o", filepath.Join(t.TempDir(), "out"), "cli_curl"},
	} {
		var code int
		var stderr string
		out := captureStdout(t, func() { code, stderr = runGonf(t, args...) })
		if code != 1 || !strings.Contains(stderr, "holds a resolved secret value") || leaksCLISecret(stderr) || leaksCLISecret(out) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want a redacted identity refusal", args, code, out, stderr)
		}
	}
}
