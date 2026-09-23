package cmd

import (
	"maps"
	"slices"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/internal/testapply"
	"github.com/snonux/gonf/resource"
)

// wantCopiedEnv is the environment a Cmd must keep after the caller mutates
// the map it passed to WithEnv.
var wantCopiedEnv = map[string]string{"GONF_COPY": "original"}

// WithEnv's contract (shared with Package): the map is copied when the option
// is applied, so a caller mutating it afterwards changes neither the
// registered command (what it runs with), its stored or recorded plan draft,
// nor the lowered plan op. The op must also not alias the draft it came from.
func TestWithEnvCopiesCallerMap(t *testing.T) {
	resource.ResetRepository()
	t.Cleanup(resource.ResetRepository)
	var recorded []resource.PlanDraft
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) { recorded = append(recorded, d) })
	t.Cleanup(func() { resource.SetPlanDraftRecorder(nil) })
	var runEnv []string
	rs := &runners.Set{Command: &runners.CommandRunners{Run: func(opts exec.Opts, _ string, _ ...string) (string, string, int, error) {
		runEnv = slices.Clone(opts.Env)
		return "", "", 0, nil
	}}}

	env := maps.Clone(wantCopiedEnv)
	Present("mybin", nil, opt.WithEnv(env), opt.WithName("env-copy"))
	// Change an existing key and add a new one: the two ways a recipe could
	// reuse its map after passing it to WithEnv.
	env["GONF_COPY"] = "mutated"
	env["GONF_ADDED"] = "added"

	drafts := resource.RegisteredPlanDrafts()
	if len(drafts) != 1 || len(recorded) != 1 {
		t.Fatalf("drafts = %d stored, %d recorded; want 1 each", len(drafts), len(recorded))
	}
	assertEnv(t, "stored draft", drafts[0].Env)
	assertEnv(t, "recorded draft", recorded[0].Env)

	draft := recorded[0]
	op, err := planHandler{}.ToOp(draft)
	if err != nil {
		t.Fatalf("ToOp: %v", err)
	}
	assertEnv(t, "plan op", op.Env)
	draft.Env["GONF_COPY"] = "draft-mutated"
	assertEnv(t, "plan op after draft mutation", op.Env)

	if err := testapply.ApplyWithRunners(rs); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !slices.Contains(runEnv, "GONF_COPY=original") || slices.Contains(runEnv, "GONF_ADDED=added") {
		// Report only the keys under test: the rest is the inherited
		// environment, which may hold secrets that must not reach logs.
		t.Fatalf("command ran with %v, want GONF_COPY=original and no GONF_ADDED",
			slices.DeleteFunc(runEnv, func(kv string) bool { return !strings.HasPrefix(kv, "GONF_") }))
	}
}

func assertEnv(t *testing.T, what string, got map[string]string) {
	t.Helper()
	if !maps.Equal(got, wantCopiedEnv) {
		t.Errorf("%s env = %v, want %v", what, got, wantCopiedEnv)
	}
}
