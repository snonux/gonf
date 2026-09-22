package resource_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
)

func TestDryRunDoesNotWrite(t *testing.T) {
	resource.ResetRepository()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(false) })

	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	file.Present(path, options.WithContent("hello"))
	if err := api.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("dry-run should not create the file")
	}
}

// TestNoteIdle pins the idle note shared by Service and Timer: a
// gate-held action is reported skipped, a converged resource ok, and
// neither counts as a change for downstream watchers (even under dry-run).
func TestNoteIdle(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	const (
		skipped = "summary: 0 ok, 0 changed, 1 skipped, 0 would-change"
		ok      = "summary: 1 ok, 0 changed, 0 skipped, 0 would-change"
	)
	for _, tc := range []struct {
		name   string
		held   bool
		dryRun bool
		want   string
	}{
		{name: "held", held: true, want: skipped},
		{name: "converged", want: ok},
		{name: "held dry-run", held: true, dryRun: true, want: skipped},
		{name: "converged dry-run", dryRun: true, want: ok},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetReport()
			resource.SetDryRun(tc.dryRun)
			resource.NoteIdle("Svc[x]", tc.held)
			var buf strings.Builder
			resource.PrintSummary(&buf)
			if got := strings.TrimSpace(buf.String()); got != tc.want {
				t.Errorf("summary = %q, want %q", got, tc.want)
			}
			if resource.AnyChanged("Svc[x]") {
				t.Error("an idle note must not count as a change")
			}
		})
	}
}
