package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestRecordPlanEmitsOrderedOpsWithGuards(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	dir := t.TempDir()
	linkPath := filepath.Join(dir, "bashrc")
	filePath := filepath.Join(dir, "taskrc")
	syncPath := filepath.Join(dir, "systemd")

	Task("demo_home", "record demo", func() {
		Link(linkPath, options.WithSymlink("/dotfiles/bashrc"))
		File(filePath, options.WithContent("set x=1\n"), options.WithMode(0o640))
		Dir(filepath.Join(dir, "empty"), options.WithMode(0o700))
		SyncDir(syncPath, filepath.Join(dir, "src", "*"), options.WithPrune)
		Package("fish")
		Command("systemctl", []string{"--user", "enable", "x.timer"},
			options.WithName("enable.x"),
			options.Unless("systemctl", []string{"--user", "is-enabled", "x.timer"}),
			options.Creates(filepath.Join(dir, "created")),
		)
	})

	ops, err := RecordPlan("demo-plan", "demo_home")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	if len(ops) < 2 || ops[0].Op != plan.KindPlan || ops[0].ID != "demo-plan" {
		t.Fatalf("header = %#v", ops[0])
	}

	kinds := make([]plan.Kind, 0, len(ops)-1)
	for _, op := range ops[1:] {
		kinds = append(kinds, op.Op)
	}
	wantKinds := []plan.Kind{
		plan.KindLink,
		plan.KindFile,
		plan.KindDir,
		plan.KindSyncDir,
		plan.KindPackage,
		plan.KindCommand,
	}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("kinds=%v want %v", kinds, wantKinds)
	}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("kinds[%d]=%s want %s (all %v)", i, kinds[i], wantKinds[i], kinds)
		}
	}

	fileOp := ops[2]
	wantB64 := base64.StdEncoding.EncodeToString([]byte("set x=1\n"))
	if fileOp.ContentB64 != wantB64 || fileOp.Mode != "0640" {
		t.Fatalf("file op = %#v", fileOp)
	}

	syncOp := ops[4]
	if syncOp.Blob == "" || !syncOp.Prune {
		t.Fatalf("sync_dir op = %#v", syncOp)
	}

	cmdOp := ops[6]
	if cmdOp.Name != "enable.x" || cmdOp.Bin != "systemctl" {
		t.Fatalf("command op = %#v", cmdOp)
	}
	if cmdOp.Unless == nil || cmdOp.Unless.Bin != "systemctl" {
		t.Fatalf("unless guard = %#v", cmdOp.Unless)
	}
	if cmdOp.Creates == "" {
		t.Fatalf("creates missing: %#v", cmdOp)
	}

	// Plan-record must not apply (no host mutations from the task body appliers).
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("link should not exist after RecordPlan: %v", err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after RecordPlan: %v", err)
	}

	encoded, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.DecodePlanBytes(encoded); err != nil {
		t.Fatalf("encode/decode: %v", err)
	}
}

func TestRecordPlanNegative(t *testing.T) {
	ResetTasks()
	Task("x", "", func() {})

	if _, err := RecordPlan("", "x"); err == nil {
		t.Fatal("expected error for empty plan id")
	}
	if _, err := RecordPlan("id"); err == nil {
		t.Fatal("expected error for no tasks")
	}
	if _, err := RecordPlan("id", "missing"); err == nil {
		t.Fatal("expected error for unknown task")
	}
}

func TestRecordPlanDoesNotLeaveRecorderEnabled(t *testing.T) {
	ResetTasks()
	Task("noop", "", func() {
		Package("helix")
	})
	if _, err := RecordPlan("p", "noop"); err != nil {
		t.Fatal(err)
	}
	if resource.PlanDraftRecording() {
		t.Fatal("recorder should be cleared after RecordPlan")
	}
}
