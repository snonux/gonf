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
)

// The dangling-dependency pre-flight also runs at RECORD time: RecordPlanTo
// is the one place every recorded plan passes through (Run, `gonf plan`,
// push, cluster and fleet all record through it) and the only place a
// `gonf plan` -> `gonf apply plan.jsonl` workflow can be protected, because
// `gonf apply` cannot tell a whole plan from a single privilege chunk. These
// tests pin that a typo'd DependsOn fails the record — before a plan exists to
// write, ship or apply — while every valid recorded shape still records and
// applies.

// registerDanglingTask registers a task that creates one valid file and then
// a command depending on a never-registered ID; it returns the paths that
// must stay absent when the record is refused.
func registerDanglingTask(t *testing.T, name, danglingID string) (independent, marker string) {
	t.Helper()
	dir := t.TempDir()
	independent = filepath.Join(dir, "independent")
	marker = filepath.Join(dir, "marker")
	Task(name, "", func() {
		File(independent, options.WithContent("x"))
		Command("touch", []string{marker}, options.DependsOn(unregisteredDep(danglingID)))
	})
	return independent, marker
}

// requireNoFiles fails when any path exists: a refused record must not have
// applied (or even partially applied) anything.
func requireNoFiles(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s exists (stat err %v): a refused plan must apply nothing", p, err)
		}
	}
}

// requireDanglingMessage checks the user-facing wording shared by every
// entry point: it names the op, the missing dependency and how to fix it,
// and never leaks the chunk bookkeeping of the plan engine.
func requireDanglingMessage(t *testing.T, err error, dep string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want a dangling-dependency refusal")
	}
	msg := err.Error()
	for _, want := range []string{"Command[touch", dep, "dangling dependency", "spelling", "register"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want it to contain %q", msg, want)
		}
	}
	for _, leak := range []string{"chunk", "no chunk"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("error = %q leaks internal wording %q", msg, leak)
		}
	}
}

func TestRecordPlanRefusesDanglingDependency(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	const dep = "File[/typo/never-registered]"
	independent, marker := registerDanglingTask(t, "dangling_record", dep)

	planDir := t.TempDir()
	ops, err := RecordPlan("dangling", planDir, "dangling_record")
	requireDanglingMessage(t, err, dep)
	if ops != nil {
		t.Fatalf("ops = %v, want none for a refused record", ops)
	}
	requireNoFiles(t, independent, marker, filepath.Join(planDir, "plan.jsonl"))
}

// TestRunRefusesDanglingDependencyBeforeApplying pins Run: the refusal now
// happens at record time, so no chunk is even attempted.
func TestRunRefusesDanglingDependencyBeforeApplying(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	const dep = "File[/typo/never-registered]"
	independent, marker := registerDanglingTask(t, "dangling_run", dep)

	requireDanglingMessage(t, Run("dangling_run"), dep)
	requireNoFiles(t, independent, marker)
}

// TestPushRefusesDanglingDependencyBeforeAnySSH pins the push path (and with
// it cluster and fleet, which record through the same RecordPlanTo): the
// refusal precedes any SSH traffic.
func TestPushRefusesDanglingDependencyBeforeAnySSH(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(ResetForTest)
	const dep = "File[/typo/never-registered]"
	registerDanglingTask(t, "dangling_push", dep)

	calls := captureSSH(t)
	err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.Doas}, "demo", "dangling_push")
	requireDanglingMessage(t, err, dep)
	if len(*calls) != 0 {
		t.Fatalf("ssh calls on a refused record: %v", remotes(*calls))
	}
}

// TestClusterAndFleetRefuseDanglingDependencyBeforeAnySSH pins the two
// inventory push paths: both record once through RecordPlanTo before fanning
// out, so a dangling dep is refused with zero SSH traffic to any host.
func TestClusterAndFleetRefuseDanglingDependencyBeforeAnySSH(t *testing.T) {
	ResetForTest()
	ResetInventory()
	t.Cleanup(ResetForTest)
	const dep = "File[/typo/never-registered]"
	registerDanglingTask(t, "dangling_fanout", dep)
	h1 := Host("h1", WithSSHUser("rex"), WithSSHHost("h1.example"))
	h2 := Host("h2", WithSSHUser("rex"), WithSSHHost("h2.example"))
	web := Cluster("web", h1, h2)
	Fleet("all", web)

	calls := captureSSH(t)
	ctx := context.Background()
	for name, run := range map[string]func() error{
		"cluster": func() error { return PushClusterRun(ctx, "web", "", 0, 0, "dangling_fanout") },
		"fleet":   func() error { return PushFleetRun(ctx, "all", "", 0, 0, "dangling_fanout") },
	} {
		requireDanglingMessage(t, run(), dep)
		if len(*calls) != 0 {
			t.Fatalf("%s: ssh calls on a refused record: %v", name, remotes(*calls))
		}
	}
}

// TestRecordPlanRefusesDanglingDependencyInsideWhenBlock covers a guarded
// task: its ops are recorded inside when_begin/when_end, and a dangling dep
// there is refused just the same (the guard is not evaluated at record time).
func TestRecordPlanRefusesDanglingDependencyInsideWhenBlock(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	marker := filepath.Join(t.TempDir(), "marker")
	Task("guarded_dangling", "", func() {
		Command("touch", []string{marker}, options.DependsOn(unregisteredDep("File[/typo/guarded]")))
	}, WhenLinux())

	_, err := RecordPlan("guarded", t.TempDir(), "guarded_dangling")
	requireDanglingMessage(t, err, "File[/typo/guarded]")
	requireNoFiles(t, marker)
}

// TestRecordPlanRefusesDependencyOnLaterPrivilegeChunk pins that the record
// time check is the full cross-chunk pre-flight ApplyChunks and PushChunks
// already run, not only the dangling half: a dep recorded after its
// dependent in a later chunk crosses the privilege boundary.
func TestRecordPlanRefusesDependencyOnLaterPrivilegeChunk(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("forward_cross_chunk", "", func() {
		later := "Command[later]"
		Command("true", nil, options.WithName("first"), options.DependsOn(unregisteredDep(later)))
		Command("true", nil, options.WithName("later"), options.WithElevate)
	})
	_, err := RecordPlan("forward", t.TempDir(), "forward_cross_chunk")
	if err == nil || !strings.Contains(err.Error(), "later chunk") {
		t.Fatalf("RecordPlan error = %v, want the later-chunk dependency refusal", err)
	}
}

// TestRecordedValidPlansStillRecordAndApply pins that the record-time
// pre-flight refuses nothing valid: chains, Multi fan-in, guarded tasks
// (when-blocks), elevated chunks with earlier-chunk deps, and absence ops all
// record, and the result applies through ApplyChunks (the same recorded plan
// `gonf plan` would write) with every dependent seeing its prerequisites.
func TestRecordedValidPlansStillRecordAndApply(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	dir := t.TempDir()
	p := func(name string) string { return filepath.Join(dir, name) }

	Task("chain_and_fan_in", "", func() {
		a := File(p("a"), options.WithContent("a"))
		b := File(p("b"), options.WithContent("b"), options.DependsOn(a))
		multi := Files([]string{p("m1"), p("m2")}, options.WithContent("m"))
		Command("sh", []string{"-c", "test -f " + p("a") + " && test -f " + p("b") + " && test -f " + p("m1") + " && test -f " + p("m2") + " && touch " + p("fan-in")},
			options.DependsOn(multi, b))
	})
	Task("guarded_valid", "", func() {
		g := File(p("g1"), options.WithContent("g"))
		Command("sh", []string{"-c", "test -f " + p("g1") + " && touch " + p("g2")}, options.DependsOn(g))
	}, WhenLinux())
	Task("elevated_then_user", "", func() {
		root := Command("touch", []string{p("root")}, options.WithName("root-step"), options.WithElevate)
		Command("sh", []string{"-c", "test -f " + p("root") + " && touch " + p("after-root")},
			options.WithName("user-step"), options.DependsOn(root))
	})
	Task("absence", "", func() {
		File(p("gone"), options.IsAbsent)
		File(p("present"), options.WithContent("p"))
	})

	ops, err := RecordPlan("valid", t.TempDir(), "chain_and_fan_in", "guarded_valid", "elevated_then_user", "absence")
	if err != nil {
		t.Fatalf("RecordPlan of valid plans: %v", err)
	}
	old := elevatedApplyRunner
	t.Cleanup(func() { elevatedApplyRunner = old })
	elevatedApplyRunner = func(_ context.Context, _ privilege.Mode, ch []plan.Op, planDir string) error {
		return ApplyPlan(ch, planDir)
	}
	if err := ApplyChunks(ops, t.TempDir(), privilege.Sudo); err != nil {
		t.Fatalf("ApplyChunks of the recorded plan: %v", err)
	}
	for _, name := range []string{"a", "b", "m1", "m2", "fan-in", "g1", "g2", "root", "after-root", "present"} {
		if _, err := os.Stat(p(name)); err != nil {
			t.Fatalf("expected %s to be applied: %v", name, err)
		}
	}
	requireNoFiles(t, p("gone"))
}

// TestRecordPlanRecordsDependencyOnResourceFromEarlierTask pins cross-task
// deps: a dependent task run after the task registering its dependency
// records fine (ID known from an earlier task body in the same session).
func TestRecordPlanRecordsDependencyOnResourceFromEarlierTask(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	var baseRes Resource
	Task("base_task", "", func() { baseRes = File(base, options.WithContent("b")) })
	Task("dependent_task", "", func() {
		Command("test", []string{"-f", base}, options.DependsOn(baseRes))
	})
	if _, err := RecordPlan("cross-task", t.TempDir(), "base_task", "dependent_task"); err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
}
