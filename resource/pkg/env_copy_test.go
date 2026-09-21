package pkg

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// wantCopiedEnv is the environment a Package must keep after the caller
// mutates the map it passed to WithEnv.
var wantCopiedEnv = map[string]string{"PKG_PATH": "https://pkgrepo.example/openbsd/"}

// WithEnv's contract (shared with Cmd): the map is copied when the option is
// applied, so a caller mutating it afterwards changes neither the registered
// package (what its probe and action run with), its stored or recorded plan
// draft, nor the lowered plan op. The op must also not alias its draft.
func TestWithEnvCopiesCallerMap(t *testing.T) {
	resource.ResetRepository()
	t.Cleanup(resource.ResetRepository)
	var recorded []resource.PlanDraft
	resource.SetPlanDraftRecorder(func(d resource.PlanDraft) { recorded = append(recorded, d) })
	t.Cleanup(func() { resource.SetPlanDraftRecorder(nil) })
	calls := stubOpenBSDEnvRunner(t)

	env := maps.Clone(wantCopiedEnv)
	Present("dtail", opt.WithEnv(env))
	// Change an existing key and add a new one: the two ways a recipe could
	// reuse its map after passing it to WithEnv.
	env["PKG_PATH"] = "https://mutated.example/"
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
	draft.Env["PKG_PATH"] = "https://draft-mutated.example/"
	assertEnv(t, "plan op after draft mutation", op.Env)

	if err := resource.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %d, want probe and action", len(*calls))
	}
	for _, env := range *calls {
		if !slices.Contains(env, "PKG_PATH="+wantCopiedEnv["PKG_PATH"]) || slices.Contains(env, "GONF_ADDED=added") {
			// Report only the keys under test: the rest is the inherited
			// environment, which may hold secrets that must not reach logs.
			t.Errorf("package manager ran with %v, want the original PKG_PATH and no GONF_ADDED",
				slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
					return !strings.HasPrefix(kv, "PKG_PATH=") && !strings.HasPrefix(kv, "GONF_")
				}))
		}
	}
}

// stubOpenBSDEnvRunner pins the OpenBSD backend and captures the environment
// of every package-manager call; the legacy runner must stay unused because
// the package has WithEnv. The seams are restored when t ends.
func stubOpenBSDEnvRunner(t *testing.T) *[][]string {
	t.Helper()
	oldRun, oldRunWith, oldDetect, oldDry := runCmd, runCmdWithEnv, detectPkgManager, resource.DryRun()
	t.Cleanup(func() {
		runCmd, runCmdWithEnv, detectPkgManager = oldRun, oldRunWith, oldDetect
		resource.SetDryRun(oldDry)
	})
	resource.SetDryRun(false)
	detectPkgManager = func() (string, error) { return "openbsd", nil }
	calls := &[][]string{}
	runCmd = func(string, ...string) (string, string, int, error) {
		t.Error("unset runner used for package with WithEnv")
		return "", "", 1, nil
	}
	runCmdWithEnv = func(env []string, bin string, args ...string) (string, string, int, error) {
		*calls = append(*calls, slices.Clone(env))
		if isPackageProbe(bin, args) {
			return "", "not installed", 1, nil
		}
		return "", "", 0, nil
	}
	return calls
}

func assertEnv(t *testing.T, what string, got map[string]string) {
	t.Helper()
	if !maps.Equal(got, wantCopiedEnv) {
		t.Errorf("%s env = %v, want %v", what, got, wantCopiedEnv)
	}
}
