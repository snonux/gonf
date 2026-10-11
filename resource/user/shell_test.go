package user

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

func TestManageShellRefusesRegistration(t *testing.T) {
	for _, shell := range []string{"", "fish", "/bin/../fish", "/bin/f:sh", "/bin/f\nsh", "/bin/f\x00sh"} {
		t.Run(shell, func(t *testing.T) {
			resource.ResetForTest()
			t.Cleanup(resource.ResetForTest)
			Present("gonfy", opt.WithShell(shell), opt.WithManageShell)
			if declerr.First() == nil || len(resource.RegisteredPlanDrafts()) != 0 || len(resource.RegisteredIDs()) != 0 {
				t.Fatal("invalid shell was registered")
			}
		})
	}
}

func TestManageShellRecordedWire(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	dependency := Present("helper")
	Present("gonfy", opt.WithShell("/usr/bin/fish"), opt.WithManageShell, opt.DependsOn(dependency))
	var draft resource.PlanDraft
	for _, d := range resource.RegisteredPlanDrafts() {
		if d.Name == "gonfy" {
			draft = d
		}
	}
	op, err := (planHandler{}).ToOp(draft)
	if err != nil {
		t.Fatal(err)
	}
	if len(op.Deps) != 1 || op.Deps[0] != dependency.ID() {
		t.Fatalf("dependencies = %v", op.Deps)
	}
	line, err := plan.EncodeOp(op)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(line), `"manage_shell":true`) {
		t.Fatalf("wire = %s", line)
	}
	decoded, err := plan.DecodeOp(line)
	if err != nil {
		t.Fatal(err)
	}
	backend, got := recordingBackend()
	if err := (planHandler{backend: backend}).Apply(decoded, plan.ApplyContext{}); err != nil {
		t.Fatal(err)
	}
	if *got == nil || !(*got).ManageShell || (*got).Shell != "/usr/bin/fish" {
		t.Fatalf("backend got %v", *got)
	}
}

func TestManageShellInvalidWireAndEnsureRefused(t *testing.T) {
	for _, shell := range []string{"", "fish", "/bin/f\nsh"} {
		t.Run(shell, func(t *testing.T) {
			draft := resource.PlanDraft{Kind: "user", ID: "User[gonfy]", Name: "gonfy", Payload: Payload{Shell: shell, ManageShell: true}}
			if _, err := (planHandler{}).ToOp(draft); err == nil {
				t.Fatal("invalid draft recorded")
			}
			backend, got := recordingBackend()
			op := draftOp(draft, draft.Payload.(Payload))
			if err := (planHandler{backend: backend}).Apply(op, plan.ApplyContext{}); err == nil || *got != nil {
				t.Fatalf("invalid apply = %v, backend = %v", err, *got)
			}
			if err := Ensure("gonfy", opt.WithShell(shell), opt.WithManageShell); err == nil {
				t.Fatal("invalid direct Ensure accepted")
			}
		})
	}
}

func TestManageShellUnsupportedDestination(t *testing.T) {
	op := plan.Op{Op: plan.KindUser, ID: "User[gonfy]", Name: "gonfy", Payload: plan.UserPayload{Shell: "/bin/sh", ManageShell: true}}
	run := func(string, ...string) (string, string, int, error) {
		t.Fatal("unsupported backend ran a command")
		return "", "", 0, nil
	}
	err := (planHandler{}).Apply(op, plan.ApplyContext{Runners: &runners.Set{User: &runners.UserRunners{Run: run, GOOS: "darwin"}}})
	if err == nil || !strings.Contains(err.Error(), "unsupported operating system darwin") {
		t.Fatalf("Apply() = %v", err)
	}
}
