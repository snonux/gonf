package user

import (
	"strings"
	"testing"

	internaluser "github.com/snonux/gonf/internal/user"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// recordingBackend returns a fake backend and a pointer to the last
// DesiredUser it received (nil when never called). Tests inject it through
// newUserWith or planHandler.backend; no package state is swapped, so tests
// using it do not interfere with each other.
func recordingBackend() (fakeBackend, **internaluser.DesiredUser) {
	var got *internaluser.DesiredUser
	return func(_ string, want internaluser.DesiredUser) error {
		got = &want
		return nil
	}, &got
}

func TestPresentRecordsManageHomeOptIn(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	Present("_dserver", opt.WithHome("/var/run/dserver"), opt.WithManageHome)
	drafts := resource.RegisteredPlanDrafts()
	if len(drafts) != 1 || !drafts[0].ManageHome || drafts[0].Home != "/var/run/dserver" {
		t.Fatalf("drafts = %#v, want one ManageHome draft", drafts)
	}
}

// TestManageHomeSurvivesTheRecordedPlanWire records, encodes, decodes, and
// applies an opted-in user, proving the destination backend receives the same
// ManageHome intent as a direct apply.
func TestManageHomeSurvivesTheRecordedPlanWire(t *testing.T) {
	backend, got := recordingBackend()
	draft := resource.PlanDraft{Kind: "user", ID: "User[_dserver]", Name: "_dserver", Home: "/var/run/dserver", ManageHome: true}
	op, err := (planHandler{}).ToOp(draft)
	if err != nil {
		t.Fatalf("ToOp() = %v", err)
	}
	line, err := plan.EncodeOp(op)
	if err != nil {
		t.Fatalf("EncodeOp() = %v", err)
	}
	if !strings.Contains(string(line), `"manage_home":true`) {
		t.Fatalf("encoded op %s lacks manage_home", line)
	}
	decoded, err := plan.DecodeOp(line)
	if err != nil {
		t.Fatalf("DecodeOp() = %v", err)
	}
	if err := (planHandler{backend: backend}).Apply(decoded, plan.ApplyContext{}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if *got == nil || !(*got).ManageHome || (*got).Home != "/var/run/dserver" {
		t.Fatalf("backend received %#v, want ManageHome with home", *got)
	}
}

// TestUserWithoutOptInEncodesExactlyAsBefore guards plan compatibility: a
// recipe that does not opt in must not emit the new field.
func TestUserWithoutOptInEncodesExactlyAsBefore(t *testing.T) {
	op, err := (planHandler{}).ToOp(resource.PlanDraft{Kind: "user", ID: "User[svc]", Name: "svc", Home: "/var/lib/svc"})
	if err != nil {
		t.Fatalf("ToOp() = %v", err)
	}
	line, err := plan.EncodeOp(op)
	if err != nil {
		t.Fatalf("EncodeOp() = %v", err)
	}
	if want := `{"op":"user","id":"User[svc]","home":"/var/lib/svc","name":"svc"}`; string(line) != want {
		t.Fatalf("encoded op = %s, want %s", line, want)
	}
}

func TestToOpRejectsInvalidManagedHomeAtRecordTime(t *testing.T) {
	for home, wantErr := range map[string]string{
		"":                "requires a home directory",
		"relative":        "must be absolute",
		"/var/run/x/":     "must be a clean path",
		"/var/run/a:b":    "contains ':'",
		"/var/./run/x":    "must be a clean path",
		"/var/run/a\nb":   "a line break",
		"/var/run/a\rb":   "a line break",
		"/var/run/a\x00b": "NUL",
	} {
		_, err := (planHandler{}).ToOp(resource.PlanDraft{Kind: "user", ID: "User[svc]", Name: "svc", Home: home, ManageHome: true})
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("ToOp(home %q) = %v, want %q", home, err, wantErr)
		}
	}
}

// TestToOpKeepsNonOptInValidationAtApplyTime pins that the record-time check
// is scoped to the opt-in: a relative creation-time home still records.
func TestToOpKeepsNonOptInValidationAtApplyTime(t *testing.T) {
	if _, err := (planHandler{}).ToOp(resource.PlanDraft{Kind: "user", ID: "User[svc]", Name: "svc", Home: "relative"}); err != nil {
		t.Fatalf("ToOp() = %v, want nil for a recipe without WithManageHome", err)
	}
}

func TestEnsureRejectsManageHomeWithoutHomeBeforeBackend(t *testing.T) {
	t.Parallel()
	backend, got := recordingBackend()
	err := newUserWith(backend, "svc", []opt.LocalUserOption{opt.WithManageHome}).apply()
	if err == nil || !strings.Contains(err.Error(), "requires a home directory") {
		t.Fatalf("Ensure() = %v", err)
	}
	if *got != nil {
		t.Fatal("invalid managed home reached backend")
	}
}
