package api

import (
	"context"
	"errors"
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

// danglingApplyCase describes one way a DependsOn target can be dangling.
type danglingApplyCase struct {
	name string
	// dangling returns the typo'd dep ID the error must name; valid is the
	// valid resource registered before the offender.
	dangling func(valid Resource) string
	// withReal also lists the valid resource as a dep, so the pre-flight
	// must single out the dangling member of a mixed dep list.
	withReal bool
}

var danglingApplyCases = []danglingApplyCase{
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

// registerDanglingApply registers an unrelated valid file and then a command
// whose DependsOn holds the case's dangling ID. It returns the dangling ID and
// the two paths that must stay absent: were the plan applied partially, the
// independent file (registered BEFORE the offender) would be created.
func registerDanglingApply(t *testing.T, tc danglingApplyCase) (dangling, independent, marker string) {
	t.Helper()
	ResetTasks()
	resource.ResetForTest()
	dir := t.TempDir()
	independent = filepath.Join(dir, "independent")
	marker = filepath.Join(dir, "marker")

	valid := File(independent, options.WithContent("x"))
	dangling = tc.dangling(valid)
	deps := []resource.Dependency{unregisteredDep(dangling)}
	if tc.withReal {
		deps = append(deps, valid)
	}
	Command("touch", []string{marker}, options.DependsOn(deps...))
	return dangling, independent, marker
}

// requireApplyDanglingMessage pins the api.Apply wording: it names the op and
// the missing dependency, hints at the fix, and neither leaks plan-engine
// bookkeeping (chunk numbers) nor doubles the "Apply:" prefix.
func requireApplyDanglingMessage(t *testing.T, err error, dangling string) {
	t.Helper()
	if err == nil {
		t.Fatal("Apply() = nil, want a dangling-dependency error")
	}
	msg := err.Error()
	for _, want := range []string{"Command[touch", dangling, "dangling dependency", "spelling", "register"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Apply() error = %q, want it to contain %q", msg, want)
		}
	}
	for _, leak := range []string{"chunk", "Apply: plan:", "Apply: Apply:"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("Apply() error = %q must not contain %q", msg, leak)
		}
	}
	if !strings.HasPrefix(msg, "Apply: ") {
		t.Fatalf("Apply() error = %q, want the Apply: prefix", msg)
	}
}

// TestApplyRejectsDanglingDependency is the o62 regression: a DependsOn ID
// matching no registered resource must fail api.Apply BEFORE anything is
// applied. plan.Apply alone treats such a dep as satisfied by an earlier
// privilege chunk, so without the pre-flight in Apply the typo was a silent
// no-op and the dependent ran unordered.
func TestApplyRejectsDanglingDependency(t *testing.T) {
	for _, tc := range danglingApplyCases {
		t.Run(tc.name, func(t *testing.T) {
			dangling, independent, marker := registerDanglingApply(t, tc)
			err := Apply()
			requireApplyDanglingMessage(t, err, dangling)
			var typed *plan.DanglingDepError
			if !errors.As(err, &typed) || typed.Dep != dangling {
				t.Fatalf("Apply() error %#v: errors.As(*plan.DanglingDepError) = %v, want the typed error for %s", err, typed, dangling)
			}
			requireNoFiles(t, independent, marker)
		})
	}
}

// TestApplyRejectsDanglingChangeWatch covers the change-gate half of the
// dependency contract through api.Apply. A typo'd OnChange target is also a
// dependency, so it surfaces as a *plan.DanglingDepError; a WatchChanges ID (a
// watch without a dependency) as a *plan.DanglingWatchError. Both are refused
// by validateApplyDeps before any mutation, with the same single "Apply: "
// prefix, registered-resource wording and no plan-engine leakage, and both stay
// reachable through errors.As.
func TestApplyRejectsDanglingChangeWatch(t *testing.T) {
	const id = "File[/typo/watched]"
	cases := []struct {
		name string
		gate options.CommandOption
		want []string
		typ  func(error) bool
	}{
		{"OnChange", options.OnChange(unregisteredDep(id)),
			[]string{"depends on " + id, "dangling dependency", "DependsOn"},
			func(err error) bool { var e *plan.DanglingDepError; return errors.As(err, &e) && e.Dep == id }},
		{"WatchChanges", options.WatchChanges(id),
			[]string{"watches " + id, "dangling watch", "OnChange/WatchChanges"},
			func(err error) bool { var e *plan.DanglingWatchError; return errors.As(err, &e) && e.Watch == id }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetTasks()
			resource.ResetForTest()
			dir := t.TempDir()
			independent := filepath.Join(dir, "independent")
			marker := filepath.Join(dir, "marker")
			File(independent, options.WithContent("x"))
			Command("touch", []string{marker}, tc.gate)

			err := Apply()
			if err == nil {
				t.Fatal("Apply() = nil, want a dangling refusal")
			}
			msg := err.Error()
			for _, want := range append(tc.want, "Apply: ", "not a registered resource", "spelling", "register that resource") {
				if !strings.Contains(msg, want) {
					t.Fatalf("Apply() error = %q, want it to contain %q", msg, want)
				}
			}
			if !strings.HasPrefix(msg, "Apply: ") || strings.Contains(msg, "plan: ") || strings.Contains(msg, "chunk") {
				t.Fatalf("Apply() error = %q, want one Apply: prefix and no plan-engine wording", msg)
			}
			if !tc.typ(err) {
				t.Fatalf("Apply() error %#v does not unwrap to the typed plan error", err)
			}
			requireNoFiles(t, independent, marker)
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

// TestApplyPlanKeepsAcceptingEarlierChunkDeps pins the intentional asymmetry
// behind the fix, for ApplyPlan (and the plan.Apply it wraps). ApplyPlan
// executes a single privilege chunk, e.g. the elevated re-exec child or a
// `gonf apply <chunk>` that receives only one chunk: a dep recorded in an
// EARLIER chunk is legitimately absent from the body and must keep applying. A
// pre-flight there would break every mixed privileged/unprivileged plan; the
// guarantee lives where the whole plan is in hand (record time, ApplyChunks,
// remote.Delivery.ToHost, api.Apply) instead.
func TestApplyPlanKeepsAcceptingEarlierChunkDeps(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	chunk := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "chunk"},
		{Op: plan.KindCommand, ID: "Command[b]", Deps: []string{"Command[a-in-earlier-chunk]"}, Payload: plan.CommandPayload{Bin: "touch", Args: []string{marker}}},
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
		{Op: plan.KindCommand, ID: "Command[first]", Elevate: true, Payload: plan.CommandPayload{Bin: "touch", Args: []string{first}}},
		{Op: plan.KindCommand, ID: "Command[second]", Deps: []string{"Command[first]"},
			Payload: plan.CommandPayload{Bin: "sh", Args: []string{"-c", "test -f " + first + " && touch " + second}}},
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
// whole-plan pre-flight in ApplyChunks refuses a dangling dep (not only a
// forward cross-chunk one) before ANY chunk is applied. ApplyChunks is
// reachable with an already-recorded plan (ops decoded from elsewhere, or
// recorded by an older gonf), which the record-time check has not seen, so it
// keeps its own pre-flight next to api.Apply's.
func TestApplyChunksRefusesDanglingDependencyBeforeAnyChunk(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "dangling"},
		{Op: plan.KindCommand, ID: "Command[a]", Payload: plan.CommandPayload{Bin: "touch", Args: []string{marker}}},
		{Op: plan.KindCommand, ID: "Command[b]", Deps: []string{"Command[typo]"}, Elevate: true, Payload: plan.CommandPayload{Bin: "true"}},
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

// TestValidateApplyDeps pins the exact semantics Apply relies on. Without
// elevated ops the whole plan is one chunk, so a dep recorded anywhere in it
// (before OR after its dependent — plan.Apply's sort orders it) passes, and
// only a dep recorded nowhere is refused. With elevated ops the plan is
// validated as the privilege chunks Apply applies: a dep on an EARLIER chunk
// passes, a dep on a LATER chunk is refused. Apply records no
// when_begin/when_end blocks (registered resources lower to a flat op list),
// so when-block plans are covered by the record-time tests in
// record_deps_test.go instead.
func TestValidateApplyDeps(t *testing.T) {
	hdr := plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}
	cmd := func(id string, deps ...string) plan.Op {
		return plan.Op{Op: plan.KindCommand, ID: id, Deps: deps, Payload: plan.CommandPayload{Bin: "true"}}
	}
	elevated := func(op plan.Op) plan.Op {
		op.Elevate = true
		return op
	}
	cases := []struct {
		name    string
		ops     []plan.Op
		wantErr string
	}{
		{name: "no deps", ops: []plan.Op{hdr, cmd("a"), cmd("b")}},
		{name: "backward dep", ops: []plan.Op{hdr, cmd("a"), cmd("b", "a")}},
		{name: "forward dep within the plan", ops: []plan.Op{hdr, cmd("b", "a"), cmd("a")}},
		{name: "dangling dep", ops: []plan.Op{hdr, cmd("a", "Command[typo]")}, wantErr: "Command[typo]"},
		{name: "dep on earlier elevated chunk", ops: []plan.Op{hdr, elevated(cmd("a")), cmd("b", "a")}},
		{name: "dep on later elevated chunk", ops: []plan.Op{hdr, cmd("b", "a"), elevated(cmd("a"))}, wantErr: "later chunk 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateApplyDeps(plan.SplitPrivilegeChunks(tc.ops), nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateApplyDeps() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.HasPrefix(err.Error(), "Apply: ") ||
				strings.Contains(err.Error(), "Apply: plan:") {
				t.Fatalf("validateApplyDeps() = %v, want a single Apply-prefixed error naming %q", err, tc.wantErr)
			}
		})
	}
}

// TestRequireDraftsForAll pins the draft/registration pairing check Apply
// runs first. The failure message must always name what is wrong: a
// registered resource without a draft, or a draft for an unregistered
// resource (an invariant the repository normally upholds, so it is exercised
// with hand-built inputs). Duplicate registered IDs are harmless and pass.
func TestRequireDraftsForAll(t *testing.T) {
	drafts := func(ids ...string) []resource.PlanDraft {
		out := make([]resource.PlanDraft, len(ids))
		for i, id := range ids {
			out[i] = resource.PlanDraft{ID: id}
		}
		return out
	}
	cases := []struct {
		name       string
		drafts     []resource.PlanDraft
		registered []string
		wantErr    string // "" = valid
	}{
		{name: "one to one", drafts: drafts("a", "b"), registered: []string{"a", "b"}},
		{name: "duplicate registered id is harmless", drafts: drafts("a"), registered: []string{"a", "a"}},
		{name: "registered without draft", drafts: drafts("a"), registered: []string{"a", "b"},
			wantErr: "registered resources without plan drafts: b"},
		{name: "draft for unregistered resource", drafts: drafts("a", "ghost"), registered: []string{"a"},
			wantErr: "plan drafts for unregistered resources: ghost"},
		{name: "several orphans are all named and sorted", drafts: drafts("z-ghost", "a-ghost"), registered: nil,
			wantErr: "plan drafts for unregistered resources: a-ghost, z-ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireDraftsForAll(tc.drafts, tc.registered)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("requireDraftsForAll() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("requireDraftsForAll() = %v, want it to contain %q", err, tc.wantErr)
			case err != nil && strings.HasSuffix(err.Error(), ": "):
				t.Fatalf("requireDraftsForAll() = %q ends with an empty list", err)
			}
		})
	}
}

// TestCrossChunkWatchRefusalYieldsToEmptyWatch pins that the class-worded
// watch refusal steps aside when plan.ValidateChangeGates would first refuse
// a gate without any watch: the Apply error must then be the empty-gate
// refusal (message and cause agree), not the later cross-chunk watch.
func TestCrossChunkWatchRefusalYieldsToEmptyWatch(t *testing.T) {
	hdr := plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}
	cmd := func(id string, elevate bool) plan.Op {
		return plan.Op{Op: plan.KindCommand, ID: id, Elevate: elevate, Payload: plan.CommandPayload{Bin: "true"}}
	}
	empty := cmd("Command[empty]", false)
	empty.IfChanged = true
	gated := cmd("Command[g]", false)
	gated.IfChanged, gated.Watch = true, []string{"Command[e]"}
	chunks := []plan.Chunk{
		{Ops: []plan.Op{hdr, empty}},
		{Elevate: true, Ops: []plan.Op{hdr, cmd("Command[e]", true)}},
		{Ops: []plan.Op{hdr, gated}},
	}
	if err := crossChunkWatchRefusal("Apply", chunks, nil); err != nil {
		t.Fatalf("crossChunkWatchRefusal() = %v, want nil ahead of an empty gate", err)
	}
	err := validateApplyDeps(chunks, nil)
	if err == nil || !strings.Contains(err.Error(), "Command[empty] is change-gated (if_changed) but watches nothing") ||
		strings.Contains(err.Error(), "Command[g]") {
		t.Fatalf("validateApplyDeps() = %v, want the empty-gate refusal", err)
	}
}
