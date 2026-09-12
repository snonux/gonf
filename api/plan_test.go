package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
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
	planDir := filepath.Join(dir, "plan-out")
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "unit.service"), []byte("[Unit]\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(dir, "bashrc")
	filePath := filepath.Join(dir, "taskrc")
	installSrc := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(installSrc, []byte("user.name=test\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	installDst := filepath.Join(dir, "out-gitconfig")
	syncPath := filepath.Join(dir, "systemd")

	Task("demo_home", "record demo", func() {
		Link(linkPath, options.WithSymlink("/dotfiles/bashrc"))
		File(filePath, options.WithContent("set x=1\n"), options.WithMode(0o640))
		InstallFile(installDst, installSrc)
		Dir(filepath.Join(dir, "empty"), options.WithMode(0o700))
		SyncDir(syncPath, filepath.Join(srcDir, "*"), options.WithPrune)
		Package("fish")
		Command("systemctl", []string{"--user", "enable", "x.timer"},
			options.WithName("enable.x"),
			options.Unless("systemctl", []string{"--user", "is-enabled", "x.timer"}),
			options.Creates(filepath.Join(dir, "created")),
		)
	})

	ops, err := RecordPlan("demo-plan", planDir, "demo_home")
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

	installOp := ops[3]
	wantInstall := base64.StdEncoding.EncodeToString([]byte("user.name=test\n"))
	if installOp.ContentB64 != wantInstall || installOp.Blob != "" {
		t.Fatalf("InstallFile op = %#v", installOp)
	}

	syncOp := ops[5]
	if syncOp.Blob != "blobs/systemd" || !syncOp.Prune {
		t.Fatalf("sync_dir op = %#v", syncOp)
	}
	blobFile := filepath.Join(planDir, "blobs", "systemd", "unit.service")
	if _, err := os.Stat(blobFile); err != nil {
		t.Fatalf("expected packaged blob file: %v", err)
	}

	cmdOp := ops[7]
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

	if _, err := RecordPlan("", "", "x"); err == nil {
		t.Fatal("expected error for empty plan id")
	}
	if _, err := RecordPlan("id", ""); err == nil {
		t.Fatal("expected error for no tasks")
	}
	if _, err := RecordPlan("id", "", "missing"); err == nil {
		t.Fatal("expected error for unknown task")
	}
}

func TestRecordPlanInlineVsBlobThreshold(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	root := t.TempDir()
	planDir := filepath.Join(root, "plan")
	smallSrc := filepath.Join(root, "small")
	largeSrc := filepath.Join(root, "large")
	if err := os.WriteFile(smallSrc, make([]byte, 100<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(largeSrc, make([]byte, 600<<10), 0o600); err != nil {
		t.Fatal(err)
	}

	Task("thresh", "", func() {
		InstallFile(filepath.Join(root, "dst-small"), smallSrc)
		InstallFile(filepath.Join(root, "dst-large"), largeSrc)
	})
	ops, err := RecordPlan("thresh", planDir, "thresh")
	if err != nil {
		t.Fatal(err)
	}

	var smallOp, largeOp *plan.Op
	for i := range ops {
		op := &ops[i]
		if op.Op != plan.KindFile {
			continue
		}
		switch {
		case op.ContentB64 != "" && op.Blob == "":
			smallOp = op
		case op.Blob != "" && op.ContentB64 == "":
			largeOp = op
		}
	}
	if smallOp == nil {
		t.Fatal("expected 100KiB file as content_b64")
	}
	if largeOp == nil {
		t.Fatal("expected 600KiB file as blob")
	}
	raw, err := base64.StdEncoding.DecodeString(smallOp.ContentB64)
	if err != nil || len(raw) != 100<<10 {
		t.Fatalf("small content len=%d err=%v", len(raw), err)
	}
}

func TestRecordPlanDoesNotLeaveRecorderEnabled(t *testing.T) {
	ResetTasks()
	Task("noop", "", func() {
		Package("helix")
	})
	if _, err := RecordPlan("p", "", "noop"); err != nil {
		t.Fatal(err)
	}
	if resource.PlanDraftRecording() {
		t.Fatal("recorder should be cleared after RecordPlan")
	}
}

func TestRecordPlanSyncDirRequiresPlanDir(t *testing.T) {
	ResetTasks()
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	Task("sync", "", func() {
		SyncDir(filepath.Join(dir, "dst"), filepath.Join(dir, "*"))
	})
	_, err := RecordPlan("p", "", "sync")
	if err == nil || !strings.Contains(err.Error(), "plan dir required") {
		t.Fatalf("want plan dir required error, got %v", err)
	}
}
