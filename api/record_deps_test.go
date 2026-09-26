package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/testutil"
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
// entry point and pins that it comes from the RECORD layer: after wrapPrefix
// (what the caller adds in front, "" for RecordPlan itself) the message starts
// with exactly one "RecordPlan: " and names the op, the missing dependency and
// how to fix it. It never leaks the plan engine's own "plan: " prefix or chunk
// bookkeeping, and never doubles a prefix. The ApplyChunks/Delivery.ToHost copy of
// the check words the same refusal "plan: op ...", so this assertion fails
// when only their (later) pre-flight catches the plan.
func requireDanglingMessage(t *testing.T, err error, dep, wrapPrefix string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want a dangling-dependency refusal")
	}
	msg := err.Error()
	if want := wrapPrefix + "RecordPlan: Command[touch"; !strings.HasPrefix(msg, want) {
		t.Fatalf("error = %q, want prefix %q (one record-time prefix, no engine wording)", msg, want)
	}
	for _, want := range []string{dep, "dangling dependency", "spelling", "register"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want it to contain %q", msg, want)
		}
	}
	for _, leak := range []string{"plan: ", "chunk", "no chunk", "RecordPlan: RecordPlan"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("error = %q leaks internal or doubled wording %q", msg, leak)
		}
	}
	var dangling *plan.DanglingDepError
	if !errors.As(err, &dangling) || dangling.Dep != dep {
		t.Fatalf("error %#v: errors.As(*plan.DanglingDepError) = %v, want the typed error for %s", err, dangling, dep)
	}
}

func TestRecordPlanRefusesDanglingDependency(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	const dep = "File[/typo/never-registered]"
	independent, marker := registerDanglingTask(t, "dangling_record", dep)

	planDir := testutil.PrivateTempDir(t)
	ops, err := RecordPlan("dangling", planDir, "dangling_record")
	requireDanglingMessage(t, err, dep, "")
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

	old := elevatedApplyRunner
	t.Cleanup(func() { elevatedApplyRunner = old })
	elevatedApplyRunner = func(context.Context, privilege.Mode, []plan.Op, string) error {
		t.Error("no chunk may be attempted for a plan refused at record time")
		return nil
	}
	requireDanglingMessage(t, Run("dangling_run"), dep, "")
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
	requireDanglingMessage(t, err, dep, "record: ")
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
		requireDanglingMessage(t, run(), dep, "record: ")
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

	_, err := RecordPlan("guarded", testutil.PrivateTempDir(t), "guarded_dangling")
	requireDanglingMessage(t, err, "File[/typo/guarded]", "")
	requireNoFiles(t, marker)
}

// TestRecordPlanRefusesDependencyOnLaterPrivilegeChunk pins that the record
// time check is the full cross-chunk pre-flight ApplyChunks and
// remote.Delivery.ToHost already run, not only the dangling half: a dep recorded after its
// dependent in a later chunk crosses the privilege boundary.
func TestRecordPlanRefusesDependencyOnLaterPrivilegeChunk(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("forward_cross_chunk", "", func() {
		later := "Command[later]"
		Command("true", nil, options.WithName("first"), options.DependsOn(unregisteredDep(later)))
		Command("true", nil, options.WithName("later"), options.WithElevate)
	})
	_, err := RecordPlan("forward", testutil.PrivateTempDir(t), "forward_cross_chunk")
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

	ops, err := RecordPlan("valid", testutil.PrivateTempDir(t), "chain_and_fan_in", "guarded_valid", "elevated_then_user", "absence")
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
	applied := []string{"a", "b", "m1", "m2", "fan-in", "root", "after-root", "present"}
	if runtime.GOOS == "linux" {
		applied = append(applied, "g1", "g2") // guarded_valid is WhenLinux
	}
	for _, name := range applied {
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
	if _, err := RecordPlan("cross-task", testutil.PrivateTempDir(t), "base_task", "dependent_task"); err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
}

// TestRecordPlanRefusalWordingIsConsistent pins the record-time wording shape
// for EVERY pre-flight refusal, not only the dangling dependency: one
// "RecordPlan: " prefix, no leaked engine prefix ("plan: plan: ..." at
// `gonf plan`, "record: plan: ..." at push, the reviewer's findings), and the
// typed plan error stays reachable through errors.As.
func TestRecordPlanRefusalWordingIsConsistent(t *testing.T) {
	for _, tc := range refusedRecordCauses() {
		if tc.name == "packaging error of a later resource" || tc.name == "declaration error in the task body" {
			continue // not pre-flight refusals; covered by the staging tests
		}
		t.Run(tc.name, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			src := newStagedSources(t)
			src.register("wording", tc.bad)

			_, err := RecordPlan("wording", testutil.PrivateTempDir(t), "wording")
			if err == nil {
				t.Fatal("RecordPlan = nil, want a refusal")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "RecordPlan: ") || strings.Count(msg, "RecordPlan:") != 1 || strings.Contains(msg, "plan: ") {
				t.Fatalf("error = %q, want exactly one leading %q and no engine %q prefix", msg, "RecordPlan: ", "plan: ")
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error = %q, want it to contain %q", msg, tc.want)
			}
			var refusal plan.Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("error %#v does not unwrap to a plan.Refusal", err)
			}
			if tc.name == "dangling watch" {
				var watch *plan.DanglingWatchError
				if !errors.As(err, &watch) || watch.Watch != "File[/typo-watch]" {
					t.Fatalf("error %#v: want a *plan.DanglingWatchError for File[/typo-watch], got %v", err, watch)
				}
				for _, want := range []string{"not a registered resource", "OnChange/WatchChanges"} {
					if !strings.Contains(msg, want) {
						t.Fatalf("error = %q, want registered-resource wording %q", msg, want)
					}
				}
			}
		})
	}
}
