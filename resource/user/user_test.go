package user

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	internaluser "github.com/snonux/gonf/internal/user"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

func TestPresentRecordsCompleteAdditiveUserDraft(t *testing.T) {
	resource.ResetForTest()
	dep := resource.Register("Package", "base", func() error { return nil })
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
		Kind: "user",
		ID:   "User[svc]",
		Name: "svc",
		Payload: Payload{
			PrimaryGroup:        "svc",
			SupplementaryGroups: []string{"wheel", "audio"},
			Home:                "/var/lib/svc",
			CreateHome:          true,
			Shell:               "/sbin/nologin",
			LoginClass:          "daemon",
			System:              true,
		},
		Deps: []string{"Package[base]"},
	}
	if !reflect.DeepEqual(drafts[0], want) {
		t.Fatalf("draft = %#v\nwant  %#v", drafts[0], want)
	}
}

func TestHandlerRoundTripAndApplyPreserveCreationIntent(t *testing.T) {
	var got internaluser.DesiredUser
	handler := planHandler{backend: fakeBackend(func(_ string, want internaluser.DesiredUser) error {
		got = want
		return nil
	})}
	draft := resource.PlanDraft{
		Kind: "user",
		ID:   "User[svc]",
		Name: "svc",
		Payload: Payload{
			PrimaryGroup:        "svc",
			SupplementaryGroups: []string{"audio", "wheel"},
			Home:                "/var/lib/svc",
			CreateHome:          true,
			Shell:               "/sbin/nologin",
			LoginClass:          "daemon",
			System:              true,
		},
		Deps: []string{"Package[base]"},
	}
	op, err := (planHandler{}).ToOp(draft)
	if err != nil {
		t.Fatalf("ToOp() = %v", err)
	}
	if op.Op != plan.KindUser || !reflect.DeepEqual(op.Deps, draft.Deps) {
		t.Fatalf("ToOp() = %#v", op)
	}
	if err := handler.Apply(op, plan.ApplyContext{}); err != nil {
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
	t.Parallel()
	called := false
	handler := planHandler{backend: fakeBackend(func(string, internaluser.DesiredUser) error { called = true; return nil })}
	err := handler.Apply(plan.Op{Op: plan.KindUser, Name: "svc", Absent: true}, plan.ApplyContext{})
	if err == nil || !strings.Contains(err.Error(), "absence is not supported") {
		t.Fatalf("Apply() = %v", err)
	}
	if called {
		t.Fatal("absence reached backend")
	}
}

func TestEnsureValidatesBeforeBackendInDryRun(t *testing.T) {
	originalDryRun := resource.DryRun()
	resource.SetDryRun(true)
	t.Cleanup(func() { resource.SetDryRun(originalDryRun) })
	called := false
	err := newUserWith(fakeBackend(func(string, internaluser.DesiredUser) error { called = true; return nil }), "-unsafe", nil).apply()
	if err == nil || !strings.Contains(err.Error(), "starts with -") {
		t.Fatalf("Ensure() = %v", err)
	}
	if called {
		t.Fatal("invalid user reached backend during dry-run")
	}
}

func TestEnsureReportsConvergedUserAsOK(t *testing.T) {
	resource.ResetReport()
	t.Cleanup(resource.ResetReport)
	converged := func(string, internaluser.DesiredUser) error { return nil }
	if err := newUserWith(fakeBackend(converged), "svc", nil).apply(); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	var report bytes.Buffer
	resource.PrintSummary(&report)
	if got, want := report.String(), "summary: 1 ok, 0 changed, 0 skipped, 0 would-change\n"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

// TestBackendForGOOSSelectsEverySupportedBackend pins the GOOS dispatch: each
// supported GOOS gets its own backend type, which is wired to the injected
// runner (observed through the first probe it runs), and an unsupported one
// reports its GOOS only when applied.
func TestBackendForGOOSSelectsEverySupportedBackend(t *testing.T) {
	t.Parallel()
	errStop := errors.New("stop after the first probe")
	for goos, want := range map[string]struct{ typ, probe string }{
		"linux":   {"user.Linux", "getent passwd svc"},
		"openbsd": {"user.OpenBSD", "getent passwd svc"},
		"netbsd":  {"user.NetBSD", "getent passwd svc"},
		"freebsd": {"user.FreeBSD", "pw usershow -n svc"},
	} {
		var got string
		run := func(command string, args ...string) (string, string, int, error) {
			got = strings.Join(append([]string{command}, args...), " ")
			return "", "", 0, errStop
		}
		backend := backendForGOOS(goos, run)
		if typ := fmt.Sprintf("%T", backend); typ != want.typ {
			t.Errorf("backendForGOOS(%q) = %s, want %s", goos, typ, want.typ)
		}
		if err := backend.Ensure("User[svc]", internaluser.DesiredUser{Name: "svc"}); !errors.Is(err, errStop) || got != want.probe {
			t.Errorf("backendForGOOS(%q) probed %q (err %v), want %q", goos, got, err, want.probe)
		}
	}
	unsupported := backendForGOOS("darwin", nil)
	if err := unsupported.Ensure("User[svc]", internaluser.DesiredUser{Name: "svc"}); err == nil || err.Error() != `user "svc": unsupported operating system darwin` {
		t.Fatalf("unsupported backendForGOOS() = %v", err)
	}
}

// TestNewUserUsesTheRunningPlatformBackend pins that the production
// constructor wires the running GOOS's backend by type: the Linux backend on
// linux, the matching BSD backend on a BSD, and the refusing stand-in
// elsewhere.
func TestNewUserUsesTheRunningPlatformBackend(t *testing.T) {
	t.Parallel()
	backend := newUser("svc", nil).backend
	want, ok := internaluser.ForGOOS(runtime.GOOS, nil)
	if !ok {
		want = unsupportedBackend{goos: runtime.GOOS}
	}
	if got, wantType := fmt.Sprintf("%T", backend), fmt.Sprintf("%T", want); got != wantType {
		t.Fatalf("newUser on %s wired %s, want %s", runtime.GOOS, got, wantType)
	}
	if runtime.GOOS == "linux" {
		if _, isLinux := backend.(internaluser.Linux); !isLinux {
			t.Fatalf("newUser on linux wired %T, want internaluser.Linux", backend)
		}
	}
}

// fakeBackend adapts a function to internaluser.Backend for tests that only
// observe what reaches the backend. It declares no capabilities.
type fakeBackend func(id string, want internaluser.DesiredUser) error

func (f fakeBackend) Ensure(id string, want internaluser.DesiredUser) error { return f(id, want) }

func (fakeBackend) Capabilities() internaluser.Capabilities { return internaluser.Capabilities{} }
