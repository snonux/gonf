package user

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

type rockyCall struct {
	command string
	args    []string
	stdout  string
	stderr  string
	code    int
	err     error
}

func scriptedRockyRunner(t *testing.T, calls []rockyCall) Runner {
	t.Helper()
	index := 0
	t.Cleanup(func() {
		if index != len(calls) {
			t.Errorf("ran %d commands, want %d (unconsumed: %v)", index, len(calls), calls[index:])
		}
	})
	return func(command string, args ...string) (string, string, int, error) {
		t.Helper()
		if index == len(calls) {
			t.Fatalf("unexpected command %s %v", command, args)
		}
		want := calls[index]
		index++
		if command != want.command || !reflect.DeepEqual(args, want.args) {
			t.Fatalf("command %s %v, want %s %v", command, args, want.command, want.args)
		}
		return want.stdout, want.stderr, want.code, want.err
	}
}

func TestRockyEnsure(t *testing.T) {
	tests := []struct {
		name  string
		want  DesiredUser
		calls []rockyCall
	}{
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
			calls: []rockyCall{
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
			name: "adds only memberships missing from existing user",
			want: DesiredUser{
				Name:                "svc",
				PrimaryGroup:        "svc",
				SupplementaryGroups: []string{"wheel", "audio", "wheel"},
			},
			calls: []rockyCall{
				{command: "getent", args: []string{"passwd", "svc"}},
				{command: "getent", args: []string{"group", "audio"}},
				{command: "getent", args: []string{"group", "wheel"}},
				{command: "id", args: []string{"--groups", "--name", "svc"}, stdout: "svc wheel\n"},
				{command: "usermod", args: []string{"--append", "--groups", "audio", "--", "svc"}},
			},
		},
		{
			name: "missing user does not create a home by default",
			want: DesiredUser{Name: "svc"},
			calls: []rockyCall{
				{command: "getent", args: []string{"passwd", "svc"}, code: 2},
				{command: "useradd", args: []string{"--no-create-home", "--", "svc"}},
			},
		},
		{
			name: "converged existing account does not mutate",
			want: DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel", "audio"}},
			calls: []rockyCall{
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
			calls: []rockyCall{
				{command: "getent", args: []string{"passwd", "svc"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := NewRocky(scriptedRockyRunner(t, tt.calls)).Ensure(tt.want); err != nil {
				t.Fatalf("Ensure() = %v", err)
			}
		})
	}
}

func TestRockyEnsureErrors(t *testing.T) {
	tests := []struct {
		name  string
		want  DesiredUser
		calls []rockyCall
		match string
	}{
		{
			name: "group lookup start failure stops before mutation",
			want: DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel"}},
			calls: []rockyCall{
				{command: "getent", args: []string{"passwd", "svc"}},
				{command: "getent", args: []string{"group", "wheel"}, err: errors.New("not found")},
			},
			match: "getent group wheel",
		},
		{
			name:  "unexpected user lookup exit is an error",
			want:  DesiredUser{Name: "svc", PrimaryGroup: "svc", SupplementaryGroups: []string{"wheel"}},
			calls: []rockyCall{{command: "getent", args: []string{"passwd", "svc"}, stderr: "nss unavailable", code: 3}},
			match: "getent passwd svc failed (exit 3)",
		},
		{
			name: "membership lookup failure does not run usermod",
			want: DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel"}},
			calls: []rockyCall{
				{command: "getent", args: []string{"passwd", "svc"}},
				{command: "getent", args: []string{"group", "wheel"}},
				{command: "id", args: []string{"--groups", "--name", "svc"}, code: 1, stderr: "no such user"},
			},
			match: "id --groups --name svc failed (exit 1)",
		},
		{
			name: "mutating command exit is returned",
			want: DesiredUser{Name: "svc"},
			calls: []rockyCall{
				{command: "getent", args: []string{"passwd", "svc"}, code: 2},
				{command: "useradd", args: []string{"--no-create-home", "--", "svc"}, code: 9, stderr: "conflict"},
			},
			match: "useradd --no-create-home -- svc failed (exit 9)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewRocky(scriptedRockyRunner(t, tt.calls)).Ensure(tt.want)
			if err == nil || !strings.Contains(err.Error(), tt.match) {
				t.Fatalf("Ensure() = %v, want %q", err, tt.match)
			}
		})
	}
}

func TestRockyEnsureDryRunProbesWithoutMutating(t *testing.T) {
	originalDryRun := resource.DryRun()
	resource.ResetReport()
	resource.SetDryRun(true)
	t.Cleanup(func() {
		resource.SetDryRun(originalDryRun)
		resource.ResetReport()
	})

	calls := []rockyCall{
		{command: "getent", args: []string{"passwd", "svc"}, code: 2},
		{command: "getent", args: []string{"group", "svc"}, code: 2},
	}
	err := NewRocky(scriptedRockyRunner(t, calls)).Ensure(DesiredUser{
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

func TestNewRockyUsesDefaultRunner(t *testing.T) {
	if got := fmt.Sprintf("%T", NewRocky(nil).run); got == "<nil>" {
		t.Fatal("NewRocky(nil) left runner nil")
	}
}
