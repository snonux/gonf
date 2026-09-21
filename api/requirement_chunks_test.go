package api

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// TestApplyChunksRequirementRefusedInFirstChunk: a mixed-privilege plan whose
// requirement (goos == openbsd) lives in the elevated chunk must be refused by
// the in-process user chunk 0, through its hoisted stub, before chunk 0 writes
// its file; the elevated runner is never called. The real host facts are
// used (ApplyPlan's DetectFacts), so the test needs a non-OpenBSD host.
func TestApplyChunksRequirementRefusedInFirstChunk(t *testing.T) {
	if runtime.GOOS == "openbsd" {
		t.Skip("needs a host where goos == openbsd does not hold")
	}
	dir := t.TempDir()
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "chunks"},
		{Op: plan.KindFile, ID: "File[user]", Path: filepath.Join(dir, "user"), Mode: "0600", ContentB64: "eAo="},
		{Op: plan.KindWhenBegin, ID: "req", All: []plan.Predicate{{Fact: "goos", Eq: "openbsd"}}, Require: "needs OpenBSD"},
		{Op: plan.KindFile, ID: "File[root]", Path: filepath.Join(dir, "root"), Mode: "0600", ContentB64: "eAo=", Elevate: true},
		{Op: plan.KindWhenEnd},
	}
	old := elevatedApplyRunner
	t.Cleanup(func() { elevatedApplyRunner = old })
	elevatedApplyRunner = func(context.Context, privilege.Mode, []plan.Op, string) error {
		t.Error("elevated runner must not be invoked once chunk 0 refused")
		return nil
	}

	err := ApplyChunks(ops, dir, privilege.Sudo)
	if err == nil || !strings.Contains(err.Error(), "chunk 0") || !strings.Contains(err.Error(), "goos="+runtime.GOOS) {
		t.Fatalf("ApplyChunks = %v, want chunk 0 refused for goos=%s", err, runtime.GOOS)
	}
	for _, name := range []string{"user", "root"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("%s written despite the refusal", name)
		}
	}
}
