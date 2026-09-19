package user

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	internaluser "github.com/snonux/gonf/internal/user"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

func TestPresentRecordsCompleteAdditiveUserDraft(t *testing.T) {
	resource.ResetForTest()
	dep := resource.Register("Package", "base", resource.ApplierFunc(func() error { return nil }))
	got := Present("svc",
		opt.WithPrimaryGroup("svc"),
		opt.WithSupplementaryGroups("wheel", "audio"),
		opt.WithHome("/var/lib/svc"),
		opt.WithCreateHome,
		opt.WithShell("/sbin/nologin"),
		opt.WithLoginClass("daemon"),
		opt.WithSystem,
		opt.DependsOn(dep),
	)
	if got.ID() != "User[svc]" {
		t.Fatalf("resource id = %q", got.ID())
	}
	drafts := resource.RegisteredPlanDrafts()
	if len(drafts) != 1 {
		t.Fatalf("draft count = %d, want 1", len(drafts))
	}
	want := resource.PlanDraft{
		Kind:                "user",
		ID:                  "User[svc]",
		Name:                "svc",
		PrimaryGroup:        "svc",
		SupplementaryGroups: []string{"wheel", "audio"},
		Home:                "/var/lib/svc",
		CreateHome:          true,
		Shell:               "/sbin/nologin",
		LoginClass:          "daemon",
		System:              true,
		Deps:                []string{"Package[base]"},
	}
	if !reflect.DeepEqual(drafts[0], want) {
		t.Fatalf("draft = %#v\nwant  %#v", drafts[0], want)
	}
}

func TestHandlerRoundTripAndApplyPreserveCreationIntent(t *testing.T) {
	original := ensureCurrent
	t.Cleanup(func() { ensureCurrent = original })
	var got internaluser.DesiredUser
	ensureCurrent = func(want internaluser.DesiredUser) error {
		got = want
		return nil
	}
	draft := resource.PlanDraft{
		Kind:                "user",
		ID:                  "User[svc]",
		Name:                "svc",
		PrimaryGroup:        "svc",
		SupplementaryGroups: []string{"audio", "wheel"},
		Home:                "/var/lib/svc",
		CreateHome:          true,
		Shell:               "/sbin/nologin",
		LoginClass:          "daemon",
		System:              true,
		Deps:                []string{"Package[base]"},
	}
	op, err := (planHandler{}).ToOp(draft)
	if err != nil {
		t.Fatalf("ToOp() = %v", err)
	}
	if op.Op != plan.KindUser || !reflect.DeepEqual(op.Deps, draft.Deps) {
		t.Fatalf("ToOp() = %#v", op)
	}
	if err := (planHandler{}).Apply(op, plan.ApplyContext{}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	want := internaluser.DesiredUser{
		Name:                "svc",
		PrimaryGroup:        "svc",
		SupplementaryGroups: []string{"audio", "wheel"},
		Home:                "/var/lib/svc",
		CreateHome:          true,
		Shell:               "/sbin/nologin",
		LoginClass:          "daemon",
		System:              true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backend want = %#v\nwant         %#v", got, want)
	}
}

func TestHandlerRejectsAbsenceBeforeBackend(t *testing.T) {
	original := ensureCurrent
	t.Cleanup(func() { ensureCurrent = original })
	called := false
	ensureCurrent = func(internaluser.DesiredUser) error { called = true; return nil }
	err := (planHandler{}).Apply(plan.Op{Op: plan.KindUser, Name: "svc", Absent: true}, plan.ApplyContext{})
	if err == nil || !strings.Contains(err.Error(), "absence is not supported") {
		t.Fatalf("Apply() = %v", err)
	}
	if called {
		t.Fatal("absence reached backend")
	}
}

func TestEnsureValidatesBeforeBackendInDryRun(t *testing.T) {
	originalEnsure, originalDryRun := ensureCurrent, resource.DryRun()
	resource.SetDryRun(true)
	t.Cleanup(func() {
		ensureCurrent = originalEnsure
		resource.SetDryRun(originalDryRun)
	})
	called := false
	ensureCurrent = func(internaluser.DesiredUser) error { called = true; return nil }
	err := Ensure("-unsafe")
	if err == nil || !strings.Contains(err.Error(), "starts with -") {
		t.Fatalf("Ensure() = %v", err)
	}
	if called {
		t.Fatal("invalid user reached backend during dry-run")
	}
}

func TestEnsureReportsConvergedUserAsOK(t *testing.T) {
	original := ensureCurrent
	ensureCurrent = func(internaluser.DesiredUser) error { return nil }
	t.Cleanup(func() { ensureCurrent = original })
	resource.ResetReport()
	if err := Ensure("svc"); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	var report bytes.Buffer
	resource.PrintSummary(&report)
	if got, want := report.String(), "summary: 1 ok, 0 changed, 0 skipped, 0 would-change\n"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestEnsureForGOOSSelectsEverySupportedBackend(t *testing.T) {
	originals := []func(internaluser.DesiredUser) error{ensureRocky, ensureOpenBSD, ensureFreeBSD, ensureNetBSD}
	t.Cleanup(func() {
		ensureRocky, ensureOpenBSD, ensureFreeBSD, ensureNetBSD = originals[0], originals[1], originals[2], originals[3]
	})
	var selected []string
	set := func(name string) func(internaluser.DesiredUser) error {
		return func(want internaluser.DesiredUser) error {
			if want.Name != "svc" {
				t.Errorf("backend %s received %q", name, want.Name)
			}
			selected = append(selected, name)
			return nil
		}
	}
	ensureRocky, ensureOpenBSD, ensureFreeBSD, ensureNetBSD = set("rocky"), set("openbsd"), set("freebsd"), set("netbsd")
	for _, goos := range []string{"linux", "openbsd", "freebsd", "netbsd"} {
		if err := ensureForGOOS(goos, internaluser.DesiredUser{Name: "svc"}); err != nil {
			t.Fatalf("ensureForGOOS(%q) = %v", goos, err)
		}
	}
	if want := []string{"rocky", "openbsd", "freebsd", "netbsd"}; !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected = %v, want %v", selected, want)
	}
	if err := ensureForGOOS("darwin", internaluser.DesiredUser{Name: "svc"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported ensureForGOOS() = %v", err)
	}
}
