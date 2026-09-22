package remote

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// preflightOps builds a two-op plan whose second op depends on dep. The
// dependent is unprivileged; when depElevated is set the dependency is a
// privileged op recorded AFTER it, i.e. in a later privilege chunk.
func preflightOps(dep string, depElevated bool) []plan.Op {
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[b]", Deps: []string{dep}},
	}
	if depElevated {
		ops = append(ops, plan.Op{Op: plan.KindCommand, Bin: "true", ID: "Command[a]", Elevate: true})
	}
	return ops
}

// TestPushToHostRunsItsOwnDependencyPreflight pins the last-line guard inside
// a Push-mode and a Preview-mode Delivery.ToHost: plans that reach them
// without having been recorded by this gonf (hand-built or recorded by an
// older release) are refused before a single SSH call is made. Every push
// test that goes through the api layer is refused earlier, at record time,
// so without this test dropping the pre-flight line in Delivery.ToHost
// would go unnoticed.
func TestPushToHostRunsItsOwnDependencyPreflight(t *testing.T) {
	restoreProbe := AssumeRemotePlanCurrent()
	t.Cleanup(restoreProbe)
	old := SSHRunner
	t.Cleanup(func() { SSHRunner = old })

	calls := 0
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		calls++
		return nil
	}
	target := PushTarget{Host: "h.example", Privilege: privilege.None}

	tests := []struct {
		name     string
		ops      []plan.Op
		dangling bool
		want     string
	}{
		{name: "dangling dependency", ops: preflightOps("File[typo]", false), dangling: true, want: "File[typo]"},
		{name: "dependency in a later privilege chunk", ops: preflightOps("Command[a]", true), want: "later chunk"},
	}
	entryPoints := map[string]func(context.Context, PushTarget, string, []plan.Op, plan.BlobReader) error{
		"Delivery(Push).ToHost":    pushToHost,
		"Delivery(Preview).ToHost": previewToHost,
	}
	for _, tc := range tests {
		for name, push := range entryPoints {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				err := push(context.Background(), target, "demo", tc.ops, nil)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s error = %v, want one containing %q", name, err, tc.want)
				}
				var dangling *plan.DanglingDepError
				if got := errors.As(err, &dangling); got != tc.dangling {
					t.Fatalf("%s error %q: typed dangling = %v, want %v", name, err, got, tc.dangling)
				}
				if calls != 0 {
					t.Fatalf("%s made %d SSH call(s) before refusing the plan", name, calls)
				}
			})
		}
	}
}

// TestPushToHostAcceptsBackwardDependencies is the positive counterpart: a
// dependency recorded in an EARLIER chunk (or the same one) still pushes.
func TestPushToHostAcceptsBackwardDependencies(t *testing.T) {
	restoreProbe := AssumeRemotePlanCurrent()
	t.Cleanup(restoreProbe)
	old := SSHRunner
	t.Cleanup(func() { SSHRunner = old })
	calls := 0
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		calls++
		return nil
	}

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[a]"},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[b]", Deps: []string{"Command[a]"}},
	}
	err := pushToHost(context.Background(), PushTarget{Host: "h.example", Privilege: privilege.None}, "demo", ops, nil)
	if err != nil {
		t.Fatalf("push with a satisfiable dependency: %v", err)
	}
	if calls == 0 {
		t.Fatal("a valid plan must reach the SSH transport")
	}
}
