package plan_test

import (
	"os"
	"testing"

	"github.com/snonux/gonf/plan"
)

func TestRecordingDefaultsOff(t *testing.T) {
	plan.ResetRecord()
	plan.SetRecording(false)
	if plan.Recording() {
		t.Fatal("expected recording off")
	}
	plan.Record(plan.Op{Op: plan.KindPackage, Name: "fish"})
	if got := plan.Recorded(); len(got) != 0 {
		t.Fatalf("Record while disabled should be no-op, got %#v", got)
	}
}

func TestRecordAndFinishRecord(t *testing.T) {
	plan.ResetRecord()
	plan.SetRecording(true)
	t.Cleanup(func() {
		plan.SetRecording(false)
		plan.ResetRecord()
	})

	plan.Record(plan.Op{Op: plan.KindLink, Path: "${HOME}/.bashrc", Symlink: "/dot/bashrc"})
	plan.Record(plan.Op{
		Op:   plan.KindCommand,
		Name: "daemon-reload",
		Bin:  "systemctl",
		Args: []string{"--user", "daemon-reload"},
		Unless: &plan.Guard{
			Bin:  "systemctl",
			Args: []string{"--user", "is-enabled", "x"},
		},
	})

	ops := plan.FinishRecord("demo")
	if len(ops) != 3 {
		t.Fatalf("len(ops)=%d, want 3", len(ops))
	}
	if ops[0].Op != plan.KindPlan || ops[0].Version != plan.RequiredVersion(ops[1:]) || ops[0].ID != "demo" {
		t.Fatalf("header = %#v", ops[0])
	}
	if ops[1].Op != plan.KindLink || ops[2].Op != plan.KindCommand {
		t.Fatalf("body kinds = %s, %s", ops[1].Op, ops[2].Op)
	}
	if ops[2].Unless == nil || ops[2].Unless.Bin != "systemctl" {
		t.Fatalf("command unless guard = %#v", ops[2].Unless)
	}

	encoded, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := plan.DecodePlanBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(ops) {
		t.Fatalf("round-trip len %d != %d", len(decoded), len(ops))
	}
}

func TestResetRecordClearsOps(t *testing.T) {
	plan.SetRecording(true)
	t.Cleanup(func() {
		plan.SetRecording(false)
		plan.ResetRecord()
	})
	plan.Record(plan.Op{Op: plan.KindDir, Path: "/tmp/x"})
	plan.ResetRecord()
	if got := plan.Recorded(); len(got) != 0 {
		t.Fatalf("after ResetRecord got %#v", got)
	}
}

func TestFormatMode(t *testing.T) {
	if got := plan.FormatMode(0o640); got != "0640" {
		t.Fatalf("FormatMode(0o640)=%q", got)
	}
	if got := plan.FormatMode(0o755 | os.ModeDir); got != "0755" {
		t.Fatalf("FormatMode(dir|0755)=%q", got)
	}
}
