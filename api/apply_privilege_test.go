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

// elevatedCall is one invocation of the faked elevated runner.
type elevatedCall struct {
	mode privilege.Mode
	ids  []string
}

// fakeElevation installs mode as the process privilege and replaces the
// elevated re-exec with run, recording every call. No sudo/doas is ever
// executed: the runner stands in for the re-exec'd `gonf apply` child. Both
// knobs are restored on cleanup (ResetForTest does not own them).
func fakeElevation(t *testing.T, mode privilege.Mode, run func(ops []plan.Op, planDir string) error) *[]elevatedCall {
	t.Helper()
	ResetForTest()
	oldRunner, oldMode := elevatedApplyRunner, processPrivilege
	t.Cleanup(func() {
		elevatedApplyRunner, processPrivilege = oldRunner, oldMode
		ResetForTest()
	})
	processPrivilege = mode
	var calls []elevatedCall
	elevatedApplyRunner = func(_ context.Context, m privilege.Mode, ops []plan.Op, planDir string) error {
		call := elevatedCall{mode: m}
		for _, op := range ops[1:] { // ops[0] is the chunk's plan header
			call.ids = append(call.ids, op.ID)
		}
		calls = append(calls, call)
		return run(ops, planDir)
	}
	return &calls
}

// TestApplySplitsElevatedOpsThroughConfiguredPrivilege pins the fix for
// api.Apply ignoring privilege splitting: a WithElevate command is cut into
// its own chunk and handed to the elevated runner with the process privilege
// mode, while the unprivileged resources around it apply in-process, in
// order. Before the fix, the elevated command ran in-process as the caller.
func TestApplySplitsElevatedOpsThroughConfiguredPrivilege(t *testing.T) {
	calls := fakeElevation(t, privilege.Doas, ApplyPlan) // child applies the chunk like `gonf apply`
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	root := filepath.Join(dir, "root")
	last := filepath.Join(dir, "last")

	f := File(first, options.WithContent("1"))
	elev := Command("sh", []string{"-c", "test -f " + first + " && touch " + root},
		options.WithElevate, options.DependsOn(f))
	Command("sh", []string{"-c", "test -f " + root + " && touch " + last}, options.DependsOn(elev))

	if err := Apply(); err != nil {
		t.Fatalf("Apply() with an elevated command: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("elevated runner ran %d times, want exactly 1", len(*calls))
	}
	got := (*calls)[0]
	if got.mode != privilege.Doas {
		t.Errorf("elevated runner mode = %v, want the process privilege %v", got.mode, privilege.Doas)
	}
	if len(got.ids) != 1 || got.ids[0] != elev.ID() {
		t.Errorf("elevated chunk = %v, want only %s", got.ids, elev.ID())
	}
	for _, p := range []string{first, root, last} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s not applied in chunk order: %v", p, err)
		}
	}
}

// TestApplyWithoutElevatedOpsNeverElevates pins that ordinary recipes keep
// the pre-split path: even with a sudo privilege mode configured, a plan
// without elevated ops never reaches the elevated runner, and an apply
// failure keeps its old unprefixed wording (no "chunk 0:" from ApplyChunks).
func TestApplyWithoutElevatedOpsNeverElevates(t *testing.T) {
	calls := fakeElevation(t, privilege.Sudo, func([]plan.Op, string) error {
		t.Error("elevated runner must not run for a plan without elevated ops")
		return nil
	})
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok")
	File(ok, options.WithContent("ok"))
	if err := Apply(); err != nil {
		t.Fatalf("Apply() unprivileged recipe: %v", err)
	}
	if _, err := os.Stat(ok); err != nil {
		t.Fatalf("unprivileged file not applied: %v", err)
	}

	resource.ResetForTest()
	Command("false", nil)
	err := Apply()
	if err == nil {
		t.Fatal("Apply() with a failing command = nil, want its error")
	}
	if strings.HasPrefix(err.Error(), "chunk ") {
		t.Errorf("Apply() error = %q, want the unsplit ApplyPlan wording without a chunk prefix", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("elevated runner ran %d times, want 0", len(*calls))
	}
}

// TestApplyOrdersChunksByDependencyNotID pins that Apply's split does not
// follow its draft order (a sort by resource ID): "Command[a-dependent]"
// sorts before "Command[b-elevated]" although it depends on it, and
// "File[...]" sorts after both although the elevated command needs it. The
// ops are put into dependency order first, so the plan applies as three
// chunks (file, elevated command, dependent) instead of being refused.
func TestApplyOrdersChunksByDependencyNotID(t *testing.T) {
	calls := fakeElevation(t, privilege.Sudo, ApplyPlan)
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	root := filepath.Join(dir, "root")
	last := filepath.Join(dir, "last")

	f := File(first, options.WithContent("1"))
	elev := Command("sh", []string{"-c", "test -f " + first + " && touch " + root},
		options.WithName("b-elevated"), options.WithElevate, options.DependsOn(f))
	Command("sh", []string{"-c", "test -f " + root + " && touch " + last},
		options.WithName("a-dependent"), options.DependsOn(elev))

	if err := Apply(); err != nil {
		t.Fatalf("Apply() = %v, want the ID-misordered plan reordered by deps", err)
	}
	if len(*calls) != 1 || len((*calls)[0].ids) != 1 || (*calls)[0].ids[0] != "Command[b-elevated]" {
		t.Fatalf("elevated runner calls = %+v, want one chunk with Command[b-elevated] only", *calls)
	}
	if _, err := os.Stat(last); err != nil {
		t.Fatalf("dependent of the elevated chunk not applied: %v", err)
	}
}

// TestApplyRefusesCrossChunkWatch is the negative case: an unprivileged
// command gated OnChange of an elevated one cannot see its change report,
// because the elevated chunk runs as a separate process. Apply refuses it
// with the Apply: wording before anything is applied — no in-process chunk,
// no elevation — instead of silently never firing the gate.
func TestApplyRefusesCrossChunkWatch(t *testing.T) {
	calls := fakeElevation(t, privilege.Sudo, func([]plan.Op, string) error {
		t.Error("elevated runner must not run when the pre-flight refuses")
		return nil
	})
	marker := filepath.Join(t.TempDir(), "marker")
	File(marker, options.WithContent("x"))
	elev := Command("true", nil, options.WithName("elevated"), options.WithElevate)
	Command("true", nil, options.WithName("gated"), options.OnChange(elev))

	err := Apply()
	if err == nil || !strings.HasPrefix(err.Error(), "Apply: ") || strings.Contains(err.Error(), "Apply: plan:") ||
		!strings.Contains(err.Error(), "change reports are chunk-local") {
		t.Fatalf("Apply() error = %v, want a single Apply: cross-chunk watch refusal", err)
	}
	var refusal plan.Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("Apply() error %#v does not unwrap to a plan.Refusal", err)
	}
	if _, serr := os.Stat(marker); !os.IsNotExist(serr) {
		t.Fatal("a chunk was applied although the pre-flight must refuse first")
	}
	if len(*calls) != 0 {
		t.Fatalf("elevated runner ran %d times, want 0", len(*calls))
	}
}

// TestApplyRefusesDanglingDepWithElevatedOps pins that the reordering does
// not swallow a typo'd dep: with an elevated op present, the dangling dep is
// still refused before anything runs.
func TestApplyRefusesDanglingDepWithElevatedOps(t *testing.T) {
	fakeElevation(t, privilege.Sudo, func([]plan.Op, string) error {
		t.Error("elevated runner must not run when the pre-flight refuses")
		return nil
	})
	Command("true", nil, options.WithName("touch-elevated"), options.WithElevate,
		options.DependsOn(unregisteredDep("Command[typo]")))
	requireApplyDanglingMessage(t, Apply(), "Command[typo]")
}

// TestApplyStopsAtFailedElevatedChunk pins that an elevation failure is
// reported (named as the elevated chunk) and later chunks are not applied.
func TestApplyStopsAtFailedElevatedChunk(t *testing.T) {
	denied := errors.New("sudo: a password is required")
	calls := fakeElevation(t, privilege.Sudo, func([]plan.Op, string) error { return denied })
	after := filepath.Join(t.TempDir(), "after")
	elev := Command("true", nil, options.WithElevate)
	File(after, options.WithContent("x"), options.DependsOn(elev))

	err := Apply()
	if !errors.Is(err, denied) || !strings.Contains(err.Error(), "(elevated)") {
		t.Fatalf("Apply() error = %v, want the elevated chunk's failure", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("elevated runner ran %d times, want 1", len(*calls))
	}
	if _, serr := os.Stat(after); !os.IsNotExist(serr) {
		t.Fatal("a chunk after the failed elevated chunk was applied")
	}
}

// TestOrderForPrivilegeSplit pins the ordering Apply splits: deps first,
// privilege classes kept together, the fewer-chunk order of the two starting
// classes, ties in incoming order, and a cycle refused with the cycle named.
func TestOrderForPrivilegeSplit(t *testing.T) {
	hdr := plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}
	op := func(id string, elevate bool, deps ...string) plan.Op {
		return plan.Op{Op: plan.KindCommand, Bin: "true", ID: id, Elevate: elevate, Deps: deps}
	}
	cases := []struct {
		name    string
		ops     []plan.Op
		want    []string
		wantErr string
	}{
		{name: "dependency before dependent", ops: []plan.Op{hdr, op("a", false, "c"), op("b", true, "c"), op("c", false)},
			want: []string{"c", "a", "b"}},
		{name: "classes grouped", ops: []plan.Op{hdr, op("a", true), op("b", false), op("c", true), op("d", false)},
			want: []string{"a", "c", "b", "d"}},
		// Starting with a's class gives a|b|c (3 chunks); starting with the
		// elevated class gives b|a,c (2 chunks), which must win.
		{name: "fewest chunks over both starting classes", ops: []plan.Op{hdr, op("a", false), op("b", true), op("c", false, "b")},
			want: []string{"b", "a", "c"}},
		{name: "equal chunk counts keep the lowest-index start", ops: []plan.Op{hdr, op("a", false), op("b", true)},
			want: []string{"a", "b"}},
		{name: "dangling dep ignored", ops: []plan.Op{hdr, op("a", true, "typo"), op("b", false)},
			want: []string{"a", "b"}},
		{name: "cycle refused", ops: []plan.Op{hdr, op("a", true, "b"), op("b", false, "a")},
			wantErr: "Apply: circular dependency: a -> b -> a"},
		{name: "cycle behind an acyclic prefix", ops: []plan.Op{hdr, op("e", true), op("x", false, "y"), op("y", false, "x"), op("z", false, "x")},
			wantErr: "Apply: circular dependency: x -> y -> x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := orderForPrivilegeSplit(tc.ops)
			if tc.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tc.wantErr) {
					t.Fatalf("orderForPrivilegeSplit() error = %v, want prefix %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("orderForPrivilegeSplit() error = %v", err)
			}
			if got[0].Op != plan.KindPlan {
				t.Fatalf("header moved: %+v", got[0])
			}
			var ids []string
			for _, o := range got[1:] {
				ids = append(ids, o.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("order = %v, want %v", ids, tc.want)
			}
		})
	}
}
