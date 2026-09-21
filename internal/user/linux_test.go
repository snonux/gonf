package user

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// linuxCase is one scripted Linux backend run: the request, the exact
// commands it must issue, and (for error cases) the text the error contains.
type linuxCase struct {
	name  string
	want  DesiredUser
	calls []scriptedCall
	match string
}

func TestLinuxEnsure(t *testing.T) {
	for _, tt := range append(linuxCreationCases(), linuxExistingCases()...) {
		t.Run(tt.name, func(t *testing.T) {
			if err := ensureAs(NewLinux(scriptedRunner(t, tt.calls)), tt.want); err != nil {
				t.Fatalf("Ensure() = %v", err)
			}
		})
	}
}

// linuxCreationCases pin the commands for an account that does not exist.
func linuxCreationCases() []linuxCase {
	return []linuxCase{
		{
			name: "creates missing groups then user with creation attributes",
			want: DesiredUser{
				Name:                "svc",
				PrimaryGroup:        "svc",
				SupplementaryGroups: []string{"wheel", "audio", "wheel"},
				Home:                "/var/lib/svc",
				CreateHome:          true,
				Shell:               "/sbin/nologin",
				System:              true,
			},
			calls: []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}, code: 2},
				{command: "getent", args: []string{"group", "audio"}, code: 2},
				{command: "groupadd", args: []string{"--", "audio"}},
				{command: "getent", args: []string{"group", "svc"}, code: 2},
				{command: "groupadd", args: []string{"--", "svc"}},
				{command: "getent", args: []string{"group", "wheel"}},
				{command: "useradd", args: []string{"--create-home", "--system", "--gid", "svc", "--groups", "audio,wheel", "--home", "/var/lib/svc", "--shell", "/sbin/nologin", "--", "svc"}},
			},
		},
		{
			name: "missing user does not create a home by default",
			want: DesiredUser{Name: "svc"},
			calls: []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}, code: 2},
				{command: "useradd", args: []string{"--no-create-home", "--", "svc"}},
			},
		},
	}
}

// linuxExistingCases pin the commands for an account that already exists.
func linuxExistingCases() []linuxCase {
	return []linuxCase{
		{
			name: "adds only memberships missing from existing user",
			want: DesiredUser{
				Name:                "svc",
				PrimaryGroup:        "svc",
				SupplementaryGroups: []string{"wheel", "audio", "wheel"},
			},
			calls: []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}},
				{command: "getent", args: []string{"group", "audio"}},
				{command: "getent", args: []string{"group", "wheel"}},
				{command: "id", args: []string{"--groups", "--name", "svc"}, stdout: "svc wheel\n"},
				{command: "usermod", args: []string{"--append", "--groups", "audio", "--", "svc"}},
			},
		},
		{
			name: "converged existing account does not mutate",
			want: DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel", "audio"}},
			calls: []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}},
				{command: "getent", args: []string{"group", "audio"}},
				{command: "getent", args: []string{"group", "wheel"}},
				{command: "id", args: []string{"--groups", "--name", "svc"}, stdout: "svc audio wheel\n"},
			},
		},
		{
			name: "existing account leaves creation-only attributes and absent primary group alone",
			want: DesiredUser{
				Name:         "svc",
				PrimaryGroup: "svc",
				Home:         "/var/lib/svc",
				CreateHome:   true,
				Shell:        "/sbin/nologin",
				System:       true,
			},
			calls: []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}},
			},
		},
	}
}

func TestLinuxEnsureErrors(t *testing.T) {
	for _, tt := range linuxErrorCases() {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureAs(NewLinux(scriptedRunner(t, tt.calls)), tt.want)
			if err == nil || !strings.Contains(err.Error(), tt.match) {
				t.Fatalf("Ensure() = %v, want %q", err, tt.match)
			}
		})
	}
}

// linuxErrorCases pin that a failing probe or command stops the run.
func linuxErrorCases() []linuxCase {
	return []linuxCase{
		{
			name: "group lookup start failure stops before mutation",
			want: DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel"}},
			calls: []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}},
				{command: "getent", args: []string{"group", "wheel"}, err: errors.New("not found")},
			},
			match: "getent group wheel",
		},
		{
			name:  "unexpected user lookup exit is an error",
			want:  DesiredUser{Name: "svc", PrimaryGroup: "svc", SupplementaryGroups: []string{"wheel"}},
			calls: []scriptedCall{{command: "getent", args: []string{"passwd", "svc"}, stderr: "nss unavailable", code: 3}},
			match: "getent passwd svc failed (exit 3)",
		},
		{
			name: "membership lookup failure does not run usermod",
			want: DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel"}},
			calls: []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}},
				{command: "getent", args: []string{"group", "wheel"}},
				{command: "id", args: []string{"--groups", "--name", "svc"}, code: 1, stderr: "no such user"},
			},
			match: "id --groups --name svc failed (exit 1)",
		},
		{
			name: "mutating command exit is returned",
			want: DesiredUser{Name: "svc"},
			calls: []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}, code: 2},
				{command: "useradd", args: []string{"--no-create-home", "--", "svc"}, code: 9, stderr: "conflict"},
			},
			match: "useradd --no-create-home -- svc failed (exit 9)",
		},
	}
}

func TestLinuxEnsureRejectsLoginClass(t *testing.T) {
	err := ensureAs(NewLinux(scriptedRunner(t, nil)), DesiredUser{
		Name:       "svc",
		LoginClass: "daemon",
	})

	if err == nil || !strings.Contains(err.Error(), "login classes are not supported") {
		t.Fatalf("Ensure() = %v", err)
	}
}

func TestLinuxEnsureDryRunProbesWithoutMutating(t *testing.T) {
	originalDryRun := resource.DryRun()
	resource.ResetReport()
	resource.SetDryRun(true)
	t.Cleanup(func() {
		resource.SetDryRun(originalDryRun)
		resource.ResetReport()
	})

	calls := []scriptedCall{
		{command: "getent", args: []string{"passwd", "svc"}, code: 2},
		{command: "getent", args: []string{"group", "svc"}, code: 2},
	}
	err := ensureAs(NewLinux(scriptedRunner(t, calls)), DesiredUser{
		Name:         "svc",
		PrimaryGroup: "svc",
		CreateHome:   true,
	})

	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	if !resource.AnyChanged("Group[svc]", "User[svc]") {
		t.Fatal("dry-run did not report the suppressed mutations")
	}
}

func TestNewLinuxUsesDefaultRunner(t *testing.T) {
	if got := fmt.Sprintf("%T", NewLinux(nil).run); got == "<nil>" {
		t.Fatal("NewLinux(nil) left runner nil")
	}
}
