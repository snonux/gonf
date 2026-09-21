package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// unregisteredDep is a resource.Dependency naming an ID no resource carries:
// exactly what a typo'd or never-registered DependsOn target looks like once
// options.DependsOn has flattened it into a dep ID.
type unregisteredDep string

func (d unregisteredDep) Dependencies() []string { return []string{string(d)} }

// TestApplyRejectsDanglingDependency is the o62 regression: a DependsOn ID
// matching no registered resource must fail api.Apply BEFORE anything is
// applied. plan.Apply alone treats such a dep as satisfied by an earlier
// privilege chunk, so without the pre-flight in Apply the typo was a silent
// no-op and the dependent ran unordered.
func TestApplyRejectsDanglingDependency(t *testing.T) {
	cases := []struct {
		name string
		// dangling returns the typo'd dep ID the error must name; valid is the
		// valid resource registered before the offender.
		dangling func(valid Resource) string
		// withReal also lists the valid resource as a dep, so the pre-flight
		// must single out the dangling member of a mixed dep list.
		withReal bool
	}{
		{
			name:     "unknown id",
			dangling: func(Resource) string { return "File[/typo/never-registered]" },
		},
		{
			name:     "near miss of a valid id",
			dangling: func(valid Resource) string { return valid.ID() + "x" },
		},
		{
			name:     "dangling member next to a valid one",
			dangling: func(Resource) string { return "File[/typo/second]" },
			withReal: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetTasks()
			resource.ResetForTest()
			dir := t.TempDir()
			independent := filepath.Join(dir, "independent")
			marker := filepath.Join(dir, "marker")

			// An unrelated resource registered BEFORE the offender: were the
			// plan applied partially, it would be created.
			valid := File(independent, options.WithContent("x"))
			deps := []resource.Dependency{unregisteredDep(tc.dangling(valid))}
			if tc.withReal {
				deps = append(deps, valid)
			}
			Command("touch", []string{marker}, options.DependsOn(deps...))

			err := Apply()
			if err == nil {
				t.Fatal("Apply() = nil, want a dangling-dependency error")
			}
			for _, want := range []string{"dangling dependency", tc.dangling(valid)} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Apply() error = %q, want it to contain %q", err, want)
				}
			}
			for _, path := range []string{independent, marker} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("%s exists (stat err %v): nothing may be applied when the pre-flight refuses", path, statErr)
				}
			}
		})
	}
}

// TestApplyAcceptsValidDependencyGraphs pins that the pre-flight rejects only
// dangling deps: chains, fan-in through a Multi, and diamonds all still apply
// in dependency order. The command dependents verify their prerequisites on
// disk, so a wrong order would fail the apply rather than pass silently.
func TestApplyAcceptsValidDependencyGraphs(t *testing.T) {
	ResetTasks()
	resource.ResetForTest()
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	c := filepath.Join(dir, "c")
	d := filepath.Join(dir, "d")
	e1 := filepath.Join(dir, "e1")
	e2 := filepath.Join(dir, "e2")

	fa := File(a, options.WithContent("a"))
	fb := File(b, options.WithContent("b"), options.DependsOn(fa))
	// Fan-in through a Multi (Files returns one), then a diamond tail:
	// c needs {a, b, e1, e2}; d needs c and a.
	multi := Files([]string{e1, e2}, options.WithContent("e"))
	fc := Command("sh", []string{"-c", "test -f " + a + " && test -f " + b + " && test -f " + e1 + " && test -f " + e2 + " && echo c > " + c},
		options.DependsOn(multi, fb))
	Command("sh", []string{"-c", "test -f " + c + " && echo d > " + d}, options.DependsOn(fc, fa))

	if err := Apply(); err != nil {
		t.Fatalf("Apply() valid graph: %v", err)
	}
	for _, p := range []string{a, b, c, d, e1, e2} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to be applied: %v", p, err)
		}
	}
}

// TestApplyChunkLevelEntryPointsKeepAcceptingEarlierChunkDeps pins the
// intentional asymmetry behind the fix. plan.Apply/ApplyPlan execute a single
// privilege chunk, e.g. the elevated re-exec child that receives only the
// elevated chunk: a dep recorded in an EARLIER chunk is legitimately absent
// from the body and must keep applying. A pre-flight there would break every
// mixed privileged/unprivileged plan; the guarantee lives in the whole-plan
// entry points (Apply, ApplyChunks, PushChunks) instead.
func TestApplyChunkLevelEntryPointsKeepAcceptingEarlierChunkDeps(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	chunk := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "chunk"},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[b]", Deps: []string{"Command[a-in-earlier-chunk]"}},
	}
	if err := ApplyPlan(chunk, dir); err != nil {
		t.Fatalf("ApplyPlan of a chunk whose dep lives in an earlier chunk: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("chunk op was not applied: %v", err)
	}
}

// TestApplyChunksMixedPrivilegeDepsStillApply is the positive partial/chunked
// counterpart: an unprivileged chunk depending on an ELEVATED chunk recorded
// before it splits into two chunks whose second body does not contain the dep,
// yet ApplyChunks must accept it (the dep is recorded in an earlier chunk)
// and run both chunks in order. The elevated runner applies the chunk for valid
// through ApplyPlan, as the re-exec'd child would.
func TestApplyChunksMixedPrivilegeDepsStillApply(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "mixed"},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{first}, ID: "Command[first]", Elevate: true},
		{Op: plan.KindCommand, Bin: "sh", Args: []string{"-c", "test -f " + first + " && touch " + second},
			ID: "Command[second]", Deps: []string{"Command[first]"}},
	}
	old := elevatedApplyRunner
	t.Cleanup(func() { elevatedApplyRunner = old })
	var elevatedRuns int
	elevatedApplyRunner = func(ctx context.Context, mode privilege.Mode, ch []plan.Op, planDir string) error {
		elevatedRuns++
		return ApplyPlan(ch, planDir)
	}
	if err := ApplyChunks(ops, dir, privilege.Sudo); err != nil {
		t.Fatalf("ApplyChunks with a dep on an earlier elevated chunk: %v", err)
	}
	if elevatedRuns != 1 {
		t.Fatalf("elevated runner ran %d times, want 1", elevatedRuns)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("dependent chunk was not applied after its elevated dep: %v", err)
	}
}

// TestApplyChunksRefusesDanglingDependencyBeforeAnyChunk pins that the
// existing whole-plan pre-flight in ApplyChunks also refuses a dangling dep
// (not only a forward cross-chunk one) before ANY chunk is applied, mirroring
// the api.Apply behaviour for the recorded-plan path.
func TestApplyChunksRefusesDanglingDependencyBeforeAnyChunk(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "dangling"},
		{Op: plan.KindCommand, Bin: "touch", Args: []string{marker}, ID: "Command[a]"},
		{Op: plan.KindCommand, Bin: "true", ID: "Command[b]", Deps: []string{"Command[typo]"}, Elevate: true},
	}
	old := elevatedApplyRunner
	t.Cleanup(func() { elevatedApplyRunner = old })
	elevatedApplyRunner = func(context.Context, privilege.Mode, []plan.Op, string) error {
		t.Error("elevated runner must not run when the pre-flight refuses")
		return nil
	}
	err := ApplyChunks(ops, dir, privilege.Sudo)
	if err == nil || !strings.Contains(err.Error(), "Command[typo]") {
		t.Fatalf("ApplyChunks error = %v, want the dangling dep named", err)
	}
	if _, serr := os.Stat(marker); !os.IsNotExist(serr) {
		t.Fatal("chunk 0 was applied although the pre-flight must refuse first")
	}
}

// TestValidateApplyDeps pins the exact semantics Apply relies on: the whole
// plan is one chunk, so a dep recorded anywhere in it (before OR after its
// dependent — plan.Apply's sort orders it, or refuses it across a when_*
// boundary later on) passes, and only a dep recorded nowhere is refused.
func TestValidateApplyDeps(t *testing.T) {
	hdr := plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}
	cmd := func(id string, deps ...string) plan.Op {
		return plan.Op{Op: plan.KindCommand, Bin: "true", ID: id, Deps: deps}
	}
	cases := []struct {
		name    string
		ops     []plan.Op
		wantErr string
	}{
		{name: "no deps", ops: []plan.Op{hdr, cmd("a"), cmd("b")}},
		{name: "backward dep", ops: []plan.Op{hdr, cmd("a"), cmd("b", "a")}},
		{name: "forward dep within the plan", ops: []plan.Op{hdr, cmd("b", "a"), cmd("a")}},
		{name: "dep on a resource inside a when block", ops: []plan.Op{
			hdr,
			{Op: plan.KindWhenBegin, All: []plan.Predicate{{Fact: "goos", Eq: "linux"}}},
			cmd("a"),
			{Op: plan.KindWhenEnd},
			cmd("b", "a"),
		}},
		{name: "dangling dep", ops: []plan.Op{hdr, cmd("a", "Command[typo]")}, wantErr: "Command[typo]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateApplyDeps(tc.ops)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateApplyDeps() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.HasPrefix(err.Error(), "Apply: ") {
				t.Fatalf("validateApplyDeps() = %v, want an Apply-prefixed error naming %q", err, tc.wantErr)
			}
		})
	}
}
