package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/testutil"
)

// TestCLIRefusesTopLevelDeclarationError: registration-time misuse in the
// recipe's main (before cli.CLI) used to end the process via logger.Fatal
// with the message on stderr. It is now a declaration error, and CLI refuses
// every invocation with it — even -version and -list — exiting 1 with the
// same message (plus the recipe line it was declared at), before any task
// body runs.
func TestCLIRefusesTopLevelDeclarationError(t *testing.T) {
	api.ResetForTest()
	t.Cleanup(api.ResetForTest)
	bodyRan := false
	api.Task("ok", "", func() { bodyRan = true })
	api.Task("ok", "", func() {}) // duplicate: the first declaration error
	api.Task("", "", func() {})   // a later one, not reported

	for _, args := range [][]string{{"ok"}, {"-list"}, {"-version"}, {"plan", "-stdout", "ok"}} {
		log := testutil.CaptureLog(t, logger.LevelInfo)
		code, stderr := runGonf(t, args...)
		if code != 1 {
			t.Fatalf("%v: exit %d, want 1", args, code)
		}
		out := log() + stderr
		if !strings.Contains(out, `Task "ok" already queued`) || strings.Contains(out, "name must not be empty") {
			t.Fatalf("%v: output %q, want only the first declaration error", args, out)
		}
		if !strings.Contains(out, "declared at ") || !strings.Contains(out, "declerr_test.go:") {
			t.Fatalf("%v: output %q, want the declaring line", args, out)
		}
	}
	if bodyRan {
		t.Fatal("a task body ran although the recipe had a declaration error")
	}
}

// TestCLIReportsTaskBodyDeclarationError: misuse inside a task body fails
// the run (exit 1) with "error: <message>" and the declaring line, and
// nothing is applied; the same body keeps failing on every run.
func TestCLIReportsTaskBodyDeclarationError(t *testing.T) {
	api.ResetForTest()
	t.Cleanup(api.ResetForTest)
	dst := filepath.Join(t.TempDir(), "never")
	api.Task("misuse", "", func() {
		api.File(dst, options.ToFileOptions(options.WithCommand("true"))...)
	})
	for range 2 {
		code, stderr := runGonf(t, "misuse")
		if code != 1 || !strings.Contains(stderr, "error: *file.File does not support WithCommand") ||
			!strings.Contains(stderr, "error: declared at ") {
			t.Fatalf("exit %d, stderr %q; want 1 and the declaration error with its location", code, stderr)
		}
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("%s was written by a refused run", dst)
	}
}
