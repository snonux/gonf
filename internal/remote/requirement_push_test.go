package remote

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// requirementPushPlan is a mixed-privilege plan: a user-level file, then an
// elevated file inside a goos == openbsd requirement block. The split puts
// the requirement in chunk 1 and a hoisted stub of it in chunk 0.
func requirementPushPlan(dir string, reqPreds []plan.Predicate) []plan.Op {
	return []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindFile, ID: "File[user]", Path: filepath.Join(dir, "user"), Mode: "0600", ContentB64: "eAo="},
		{Op: plan.KindWhenBegin, ID: "req", All: reqPreds, Require: "needs OpenBSD"},
		{Op: plan.KindFile, ID: "File[root]", Path: filepath.Join(dir, "root"), Mode: "0600", ContentB64: "eAo=", Elevate: true},
		{Op: plan.KindWhenEnd},
	}
}

// TestPushToHostRequirementRefusedInFirstChunk: the fake SSH runner plays a
// FreeBSD destination by decoding each streamed chunk and applying it with
// the plan engine. Chunk 0 must carry the requirement stub and refuse before
// writing its user file, so the elevated chunk is never sent.
func TestPushToHostRequirementRefusedInFirstChunk(t *testing.T) {
	old := SSHRunner
	restoreProbe := AssumeRemotePlanCurrent()
	t.Cleanup(func() { SSHRunner = old; restoreProbe() })

	dir := t.TempDir()
	var applyCalls, elevatedCalls int
	SSHRunner = func(_ context.Context, stdin io.Reader, argv []string) error {
		applyCalls++
		if strings.Contains(argv[len(argv)-1], "sudo") {
			elevatedCalls++
		}
		payload, err := plan.DecodePush(stdin, t.TempDir())
		if err != nil {
			return err
		}
		return plan.Apply(payload.Ops, plan.Facts{GOOS: "freebsd"}, payload.PlanDir)
	}

	ops := requirementPushPlan(dir, []plan.Predicate{{Fact: "goos", Eq: "openbsd"}})
	err := pushToHost(context.Background(), PushTarget{Host: "h.example", Privilege: privilege.Sudo}, "demo", ops, nil)
	if err == nil || !strings.Contains(err.Error(), "chunk 0") || !strings.Contains(err.Error(), "goos=freebsd") {
		t.Fatalf("push = %v, want chunk 0 refused for goos=freebsd", err)
	}
	if applyCalls != 1 || elevatedCalls != 0 {
		t.Fatalf("remote apply calls = %d (elevated %d); want only chunk 0, never the elevated chunk", applyCalls, elevatedCalls)
	}
	for _, name := range []string{"user", "root"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("%s written despite the refusal", name)
		}
	}
}

// TestPushToHostBadRequirementScopeSendsNothing: a requirement whose own
// predicate is path_exists is refused by the controller pre-flight, before
// any SSH traffic at all.
func TestPushToHostBadRequirementScopeSendsNothing(t *testing.T) {
	old := SSHRunner
	restoreProbe := AssumeRemotePlanCurrent()
	t.Cleanup(func() { SSHRunner = old; restoreProbe() })

	calls := 0
	SSHRunner = func(context.Context, io.Reader, []string) error { calls++; return nil }
	dir := t.TempDir()
	ops := requirementPushPlan(dir, []plan.Predicate{{PathExists: filepath.Join(dir, "marker")}})
	err := pushToHost(context.Background(), PushTarget{Host: "h.example", Privilege: privilege.Sudo}, "demo", ops, nil)
	if err == nil || !strings.Contains(err.Error(), "requirement req itself uses (path_exists") {
		t.Fatalf("push = %v, want the requirement-scope refusal", err)
	}
	if calls != 0 {
		t.Fatalf("SSH runner called %d times for a refused plan", calls)
	}
}
