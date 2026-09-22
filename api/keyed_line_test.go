package api

import (
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
)

// TestHeaderVersionFollowsKeyedLines pins the on-demand v23 header: a plan
// with a WithKeyedLine edit needs a destination that honours keyed_lines,
// while an ordinary line edit keeps the v21 header a v0.15.0 destination
// applies.
func TestHeaderVersionFollowsKeyedLines(t *testing.T) {
	record := func(body func()) []plan.Op {
		t.Helper()
		ResetForTest()
		t.Cleanup(ResetForTest)
		Task("t", "", body)
		ops, err := RecordPlanTo("keyed", plan.NewMemoryStore(), "t")
		if err != nil {
			t.Fatal(err)
		}
		return ops
	}
	ops := record(func() { File("/etc/plain", options.WithLine("x=1")) })
	if ops[0].Version != plan.VersionSensitive-1 {
		t.Fatalf("line-edit plan header v%d, want v%d", ops[0].Version, plan.VersionSensitive-1)
	}
	ops = record(func() { File("/etc/keyed", options.WithKeyedLine("x=", "x=1")) })
	if ops[0].Version != plan.VersionKeyedLines {
		t.Fatalf("keyed plan header v%d, want v%d", ops[0].Version, plan.VersionKeyedLines)
	}
	if op := opByPath(t, ops, "/etc/keyed"); len(op.KeyedLines) != 1 || op.KeyedLines[0] != (plan.KeyedLine{Key: "x=", Line: "x=1"}) {
		t.Fatalf("keyed op = %+v", op)
	}
}
