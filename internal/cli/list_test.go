package cli

import (
	"io"
	"os"
	"testing"

	"github.com/snonux/gonf/api"
)

// TestCLIListAliases pins the -list rows for aliases: an alias with its own
// description lists exactly like an ordinary task (so migrating a Run-only
// wrapper to Alias keeps -list byte-identical), an alias without one says
// what it aliases, and a broken alias is not listed at all.
func TestCLIListAliases(t *testing.T) {
	api.ResetForTest()
	t.Cleanup(api.ResetForTest)
	api.Task("home_agents", "Install agent symlinks", func() {})
	api.Task("plain", "", func() {})
	api.Alias("home_prompts", "Legacy alias for home_agents", "home_agents")
	api.Alias("short", "", "home_agents")
	api.Alias("broken", "", "missing")

	got := captureList(t)
	want := "home_agents\tInstall agent symlinks\n" +
		"home_prompts\tLegacy alias for home_agents\n" +
		"plain\n" +
		"short\talias of home_agents\n"
	if got != want {
		t.Fatalf("-list output:\n%q\nwant:\n%q", got, want)
	}
}

// captureList runs `gonf -list` and returns its stdout.
func captureList(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldArgs := os.Stdout, os.Args
	t.Cleanup(func() { os.Stdout, os.Args = oldOut, oldArgs })
	os.Stdout = w
	os.Args = []string{"gonf", "-list"}
	code := CLI()
	_ = w.Close()
	os.Stdout = oldOut
	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("-list exit %d", code)
	}
	return string(out)
}
