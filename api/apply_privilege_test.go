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
// because the elevated chunk applies as its own plan.Apply run (here a
// separate sudo process) with its own change report. Apply refuses it
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
	requireClassWatchRefusal(t, err,
		"Apply: Command[gated] (unprivileged) watches Command[elevated] (elevated); change reports are not carried across privilege classes")
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
// reported in Apply's terms (the privilege class and resources of the failed
// chunk, no chunk index) and later chunks are not applied.
func TestApplyStopsAtFailedElevatedChunk(t *testing.T) {
	denied := errors.New("sudo: a password is required")
	calls := fakeElevation(t, privilege.Sudo, func([]plan.Op, string) error { return denied })
	after := filepath.Join(t.TempDir(), "after")
	elev := Command("true", nil, options.WithElevate)
	File(after, options.WithContent("x"), options.DependsOn(elev))

	err := Apply()
	want := "Apply: elevated resources " + elev.ID() + ": "
	if !errors.Is(err, denied) || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("Apply() error = %v, want the elevated chunk's failure", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("elevated runner ran %d times, want 1", len(*calls))
	}
	if _, serr := os.Stat(after); !os.IsNotExist(serr) {
		t.Fatal("a chunk after the failed elevated chunk was applied")
	}
}

// requireClassWatchRefusal checks that err is a single-prefix Apply refusal
// starting with want and worded without chunk bookkeeping.
func requireClassWatchRefusal(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "Apply: plan:") ||
		strings.Contains(err.Error(), "chunk 0") || strings.Contains(err.Error(), "chunk 1") {
		t.Fatalf("Apply() error = %v, want prefix %q without chunk indexes", err, want)
	}
}

// TestApplyKeepsWatcherWithWatchedAcrossElevation is the review repro:
// File[x]; an elevated Command[e] that needs File[x]; File[f]; and an
// unprivileged Command[g] gated OnChange(File[f]) that also needs Command[e].
// The class-greedy sort used to emit File[f] and File[x] together before
// Command[e], leaving the gate in a later chunk than File[f], and the
// pre-flight refused a valid recipe. Apply must run it as File[x] |
// Command[e] | File[f], Command[g] and fire the gate.
func TestApplyKeepsWatcherWithWatchedAcrossElevation(t *testing.T) {
	calls := fakeElevation(t, privilege.Sudo, ApplyPlan)
	dir := t.TempDir()
	x := File(filepath.Join(dir, "x"), options.WithContent("x"))
	e := Command("true", nil, options.WithName("e"), options.WithElevate, options.DependsOn(x))
	f := File(filepath.Join(dir, "f"), options.WithContent("f"))
	fired := filepath.Join(dir, "fired")
	Command("touch", []string{fired}, options.WithName("g"), options.OnChange(f), options.DependsOn(e))

	if err := Apply(); err != nil {
		t.Fatalf("Apply() = %v, want the watch kept in one chunk", err)
	}
	if len(*calls) != 1 || len((*calls)[0].ids) != 1 || (*calls)[0].ids[0] != e.ID() {
		t.Fatalf("elevated calls = %+v, want one chunk with %s only", *calls, e.ID())
	}
	if _, err := os.Stat(fired); err != nil {
		t.Fatalf("gate of Command[g] did not fire on the File[f] change: %v", err)
	}
}

// TestApplyRefusesWatchForcedApartByElevation pins the negative case of the
// same shape: Command[g] watches File[f] but needs an elevated Command[e]
// that itself needs File[f], so File[f] must apply before the elevation and
// Command[g] after it. No order can keep the gate with its watch, and the
// refusal says so in privilege-class terms before anything applies.
func TestApplyRefusesWatchForcedApartByElevation(t *testing.T) {
	calls := refuseElevation(t, privilege.Sudo)
	marker := filepath.Join(t.TempDir(), "f")
	f := File(marker, options.WithContent("f"))
	e := Command("true", nil, options.WithName("e"), options.WithElevate, options.DependsOn(f))
	Command("true", nil, options.WithName("g"), options.OnChange(f), options.DependsOn(e))

	requireClassWatchRefusal(t, Apply(), "Apply: Command[g] (unprivileged) watches "+f.ID()+
		" (unprivileged), but their dependencies need resources of the other privilege class applied between the two")
	requireNothingApplied(t, marker, calls)
}

// TestApplyRefusesElevatedWatcherOfUnprivilegedChange pins that change state
// is not carried across chunks in the other direction either: an elevated
// command gated on an unprivileged File (like an elevated daemon-reload
// watching a user unit file) is refused, as Run refuses it.
func TestApplyRefusesElevatedWatcherOfUnprivilegedChange(t *testing.T) {
	calls := refuseElevation(t, privilege.Sudo)
	marker := filepath.Join(t.TempDir(), "unit")
	f := File(marker, options.WithContent("unit"))
	Command("true", nil, options.WithName("reload"), options.WithElevate, options.OnChange(f))

	requireClassWatchRefusal(t, Apply(), "Apply: Command[reload] (elevated) watches "+f.ID()+
		" (unprivileged); change reports are not carried across privilege classes")
	requireNothingApplied(t, marker, calls)
}

// TestApplyNamesOnlyTheUnsatisfiableWatch pins that one watch no order can
// satisfy does not cost the others their chunk: Command[a] watches File[b]
// but needs the elevated Command[e], which needs File[b] (unsatisfiable),
// while Command[c] watches File[d] (WatchChanges, no dep), which needs the
// elevated Command[f] (satisfiable: c joins d's chunk). Dropping every watch
// when one fails used to split c from d and name that innocent watch; the
// refusal must name a/b and not c/d.
func TestApplyNamesOnlyTheUnsatisfiableWatch(t *testing.T) {
	calls := refuseElevation(t, privilege.Sudo)
	dir := t.TempDir()
	b := File(filepath.Join(dir, "b"), options.WithContent("b"))
	e := Command("true", nil, options.WithName("e"), options.WithElevate, options.DependsOn(b))
	Command("true", nil, options.WithName("a"), options.OnChange(b), options.DependsOn(e))
	f := Command("true", nil, options.WithName("f"), options.WithElevate)
	d := File(filepath.Join(dir, "d"), options.WithContent("d"), options.DependsOn(f))
	Command("true", nil, options.WithName("c"), options.WatchChanges(d.ID()))

	err := Apply()
	requireClassWatchRefusal(t, err, "Apply: Command[a] (unprivileged) watches "+b.ID()+" (unprivileged), but")
	if strings.Contains(err.Error(), "Command[c]") || strings.Contains(err.Error(), d.ID()) {
		t.Fatalf("Apply() error = %v names the satisfiable watch c -> d", err)
	}
	requireNothingApplied(t, filepath.Join(dir, "b"), calls)
}

// TestApplyNamesTheKeptWatchAConflictIsWithTogether pins the refusal of a
// watch that would fit on its own but not together with a watch kept before
// it: Command[a] needs Command[d] and watches Command[b]; Command[c] needs
// the elevated Command[e], which needs Command[b], and watches Command[d].
// Keeping a with b puts d no later than b, while c with d needs d after the
// elevation that follows b. Alone, c's watch fits (nothing forces d early),
// so the refusal must not claim the pair's own dependencies force it apart;
// it names the kept watch it conflicts with.
func TestApplyNamesTheKeptWatchAConflictIsWithTogether(t *testing.T) {
	refuseElevation(t, privilege.Sudo)
	b := Command("true", nil, options.WithName("b"))
	d := Command("true", nil, options.WithName("d"))
	e := Command("true", nil, options.WithName("e"), options.WithElevate, options.DependsOn(b))
	Command("true", nil, options.WithName("a"), options.OnChange(b), options.DependsOn(d))
	Command("true", nil, options.WithName("c"), options.OnChange(d), options.DependsOn(e))

	requireClassWatchRefusal(t, Apply(), "Apply: Command[c] (unprivileged) watches Command[d] (unprivileged), "+
		"but together with the change watch Command[a] watching Command[b], their dependencies need resources")
}

// TestApplyListsOnlyTheWatchesAConflictNeeds pins that the "together with"
// list is minimal (the round-6 review repro). File[b]; Command[c] watches
// File[b]; the elevated Command[e] needs Command[c]; Command[a] needs
// Command[e] and watches File[b]; Command[d] watches Command[a]. Watches are
// kept in declaration order (by ID): a with b, then d with a; c with b no
// longer fits, since b sits in a's chunk after e while e needs c. d's watch
// shares the conflicting component but removing it would fix nothing, so
// the refusal of c's watch must name a's watch only. WatchChanges, unlike
// OnChange, adds no dependency, so each of these watches would fit alone.
func TestApplyListsOnlyTheWatchesAConflictNeeds(t *testing.T) {
	refuseElevation(t, privilege.Sudo)
	b := File(filepath.Join(t.TempDir(), "b"), options.WithContent("b"))
	c := Command("true", nil, options.WithName("c"), options.WatchChanges(b.ID()))
	e := Command("true", nil, options.WithName("e"), options.WithElevate, options.DependsOn(c))
	a := Command("true", nil, options.WithName("a"), options.WatchChanges(b.ID()), options.DependsOn(e))
	Command("true", nil, options.WithName("d"), options.WatchChanges(a.ID()))

	err := Apply()
	requireClassWatchRefusal(t, err, "Apply: Command[c] (unprivileged) watches "+b.ID()+" (unprivileged), "+
		"but together with the change watch Command[a] watching "+b.ID()+", their dependencies")
	if strings.Contains(err.Error(), "Command[d] watching") {
		t.Fatalf("Apply() error = %v lists Command[d]'s watch, which the conflict does not need", err)
	}
}
