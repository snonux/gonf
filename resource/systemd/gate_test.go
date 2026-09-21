package systemd

import (
	"reflect"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

// TestDaemonReloadDraftWatch pins the daemon-reload draft wiring, which
// deliberately differs from the other gated kinds: Watch is always the
// merged OnChange + legacy WithWatch list, falling back to the DependsOn ids
// when neither supplies any, and it is recorded even when the gate is
// unarmed. Recorded plans depend on this shape.
func TestDaemonReloadDraftWatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      []opt.DaemonReloadOption
		wantGated bool
		wantWatch []string
	}{
		{name: "plain", opts: nil},
		{name: "ungated deps still recorded as watch",
			opts:      []opt.DaemonReloadOption{opt.DependsOn(dep("File[a]"))},
			wantWatch: []string{"File[a]"}},
		{name: "legacy IfChanged falls back to deps",
			opts:      []opt.DaemonReloadOption{opt.IfChanged, opt.DependsOn(dep("File[a]"))},
			wantGated: true, wantWatch: []string{"File[a]"}},
		{name: "IfChanged with WithWatch",
			opts:      []opt.DaemonReloadOption{opt.IfChanged, opt.WithWatch("File[b]", "File[b]")},
			wantGated: true, wantWatch: []string{"File[b]"}},
		{name: "OnChange then WithWatch merge",
			opts:      []opt.DaemonReloadOption{opt.WatchChanges("File[c]"), opt.WithWatch("File[b]")},
			wantGated: true, wantWatch: []string{"File[c]", "File[b]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &DaemonReloadResource{}
			for _, o := range tc.opts {
				o.Apply(d)
			}
			got := d.planDraft("DaemonReload[system]")
			if got.IfChanged != tc.wantGated || !reflect.DeepEqual(got.Watch, tc.wantWatch) {
				t.Errorf("draft IfChanged=%t Watch=%#v, want %t/%#v",
					got.IfChanged, got.Watch, tc.wantGated, tc.wantWatch)
			}
		})
	}
}

// dep is a resource.Dependency naming a single id, for DependsOn options.
type dep string

func (d dep) Dependencies() []string { return []string{string(d)} }

// TestDaemonReloadUnknownWatchSkips pins that a gate watching an id nothing
// ever noted holds the reload and reports it skipped.
func TestDaemonReloadUnknownWatchSkips(t *testing.T) {
	resource.ResetRepository()
	old := runCmd
	t.Cleanup(func() { runCmd = old })
	called := false
	runCmd = func(string, ...string) (string, string, int, error) {
		called = true
		return "", "", 0, nil
	}

	Present(opt.WatchChanges("File[never-noted]"))
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("daemon-reload ran although no watched id changed")
	}
	var buf strings.Builder
	resource.PrintSummary(&buf)
	if !strings.Contains(buf.String(), "summary: 0 ok, 0 changed, 1 skipped, 0 would-change") {
		t.Errorf("summary = %q", buf.String())
	}
}
