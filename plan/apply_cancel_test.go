package plan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Canceling the ctx of ApplyWithContext while a long-running command op
// runs must kill it promptly, stop the apply before the next op and return
// an error wrapping context.Canceled.
func TestApplyWithContextCancelKillsRunningCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "after")
	ops := []Op{
		header(),
		{Op: KindCommand, Bin: "sleep", Args: []string{"30"}, ID: "Command[sleep]"},
		{Op: KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[after]"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	err := ApplyWithContext(ctx, ops, Facts{}, "")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("canceled apply returned after %v, want prompt return", elapsed)
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want context.Canceled from line 2", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("op after the canceled command must not run (stat: %v)", statErr)
	}
}

// An already canceled ctx applies nothing: the refusal comes before the
// first op, named by its line.
func TestApplyWithContextCanceledAppliesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	ops := []Op{header(), {Op: KindEnsureDir, Path: dir, Mode: "0750"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ApplyWithContext(ctx, ops, Facts{}, "")
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "canceled before line 2") {
		t.Fatalf("err = %v, want canceled before line 2", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("canceled apply must not create %s (stat: %v)", dir, statErr)
	}
}

// Negative cases: a live ctx applies normally, and once the apply returned
// its ctx no longer governs later commands (the internal/exec binding is
// restored), so a canceled apply ctx cannot break a subsequent plain Apply.
func TestApplyWithContextLiveCtxAndRestoredBinding(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	ctx, cancel := context.WithCancel(context.Background())
	ops := []Op{header(), {Op: KindCommand, Bin: "touch", Args: []string{first}, ID: "Command[first]"}}
	if err := ApplyWithContext(ctx, ops, Facts{}, ""); err != nil {
		t.Fatalf("live ctx: %v", err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("live ctx must run the command: %v", err)
	}

	cancel()
	second := filepath.Join(root, "second")
	ops = []Op{header(), {Op: KindCommand, Bin: "touch", Args: []string{second}, ID: "Command[second]"}}
	if err := Apply(ops, Facts{}, ""); err != nil {
		t.Fatalf("Apply after a canceled ApplyWithContext ctx: %v", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("Apply must run the command: %v", err)
	}
}
