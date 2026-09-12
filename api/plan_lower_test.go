package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestRecordPlanLowersWhenEnsureDirLinkIfExists(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	cursor := filepath.Join(dir, ".cursor")
	notes := filepath.Join(dir, "Notes")
	linkPath := filepath.Join(dir, "QuickEdit", "Notes")

	// WhenLinux must emit when_begin even if we force a non-matching Activate filter.
	Activate(Facts{GOOS: "windows", Profile: "unknown"})

	Task("home_gated", "linux gated", func() {
		EnsureDir(cursor, options.WithMode(0o750))
		LinkIfExists(linkPath, notes)
		Package("fish")
	}, WhenLinux(), WhenProfile("fedora"))

	ops, err := RecordPlan("gated", "", "home_gated")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindWhenBegin,
		plan.KindEnsureDir,
		plan.KindLinkIfExists,
		plan.KindPackage,
		plan.KindWhenEnd,
	}
	if len(ops) != len(wantKinds) {
		t.Fatalf("ops=%v want %d kinds", ops, len(wantKinds))
	}
	for i, k := range wantKinds {
		if ops[i].Op != k {
			t.Fatalf("ops[%d]=%s want %s (all %#v)", i, ops[i].Op, k, ops)
		}
	}

	begin := ops[1]
	if begin.ID != "when.home_gated" || len(begin.All) != 2 {
		t.Fatalf("when_begin = %#v", begin)
	}
	if begin.All[0] != (plan.Predicate{Fact: "goos", Eq: "linux"}) {
		t.Fatalf("pred0 = %#v", begin.All[0])
	}
	if begin.All[1] != (plan.Predicate{Fact: "profile", Eq: "fedora"}) {
		t.Fatalf("pred1 = %#v", begin.All[1])
	}

	ensure := ops[2]
	if ensure.Path != cursor || ensure.Mode != "0750" {
		t.Fatalf("ensure_dir = %#v", ensure)
	}
	if _, err := os.Stat(cursor); !os.IsNotExist(err) {
		t.Fatalf("ensure_dir must not create on controller: %v", err)
	}

	linkOp := ops[3]
	if linkOp.Path != linkPath || linkOp.Target != notes {
		t.Fatalf("link_if_exists = %#v", linkOp)
	}
}

func TestRecordPlanWhenPathExistsHelper(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	gate := filepath.Join(dir, "commands")
	cursor := filepath.Join(dir, ".cursor")

	Task("home_agents", "", func() {
		WhenPathExists(gate, func() {
			EnsureDir(cursor, options.WithMode(0o750))
			Link(filepath.Join(cursor, "commands"), options.WithSymlink(gate))
		})
	})

	ops, err := RecordPlan("agents", "", "home_agents")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	wantKinds := []plan.Kind{
		plan.KindPlan,
		plan.KindWhenBegin,
		plan.KindEnsureDir,
		plan.KindLink,
		plan.KindWhenEnd,
	}
	if len(ops) != len(wantKinds) {
		t.Fatalf("ops kinds=%v", opsKinds(ops))
	}
	for i, k := range wantKinds {
		if ops[i].Op != k {
			t.Fatalf("ops[%d]=%s want %s", i, ops[i].Op, k)
		}
	}
	if len(ops[1].All) != 1 || ops[1].All[0].PathExists != gate {
		t.Fatalf("path_exists when = %#v", ops[1])
	}
	if _, err := os.Stat(cursor); !os.IsNotExist(err) {
		t.Fatalf("should not create cursor on controller: %v", err)
	}
}

func TestWhenPathExistsLocalRunsOnlyIfPresent(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	present := filepath.Join(dir, "yes")
	if err := os.Mkdir(present, 0o750); err != nil {
		t.Fatal(err)
	}

	var ranMissing, ranPresent bool
	WhenPathExists(missing, func() { ranMissing = true })
	WhenPathExists(present, func() { ranPresent = true })
	if ranMissing {
		t.Fatal("missing path should skip")
	}
	if !ranPresent {
		t.Fatal("present path should run")
	}
}

func TestRecordPlanOpaqueWhenRequiresLocalPass(t *testing.T) {
	ResetTasks()
	Task("custom", "", func() {
		Package("x")
	}, When(func(f Facts) bool { return f.GOOS == "plan9" }))

	_, err := RecordPlan("p", "", "custom")
	if err == nil {
		t.Fatal("expected error for failing opaque When")
	}
}

func TestLinkIfExistsDoesNotProbeDuringRecord(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	target := filepath.Join(dir, "missing-target")

	Task("map", "", func() {
		LinkIfExists(path, target)
	})
	ops, err := RecordPlan("p", "", "map")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[1].Op != plan.KindLinkIfExists {
		t.Fatalf("ops=%#v", ops)
	}
	if ops[1].Target != target {
		t.Fatalf("target=%q", ops[1].Target)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("must not create NoLink on controller: %v", err)
	}
}

func opsKinds(ops []plan.Op) []plan.Kind {
	out := make([]plan.Kind, len(ops))
	for i, op := range ops {
		out[i] = op.Op
	}
	return out
}
