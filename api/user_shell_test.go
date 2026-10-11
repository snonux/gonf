package api

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

func TestUserManageShellPlanRoundTripAndApply(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("shell", "", func() {
		helper := User("helper")
		User("gonfy", options.WithShell("/usr/bin/fish"), options.WithManageShell, DependsOn(helper))
		User("plain", WithShell("/bin/sh"))
	})
	ops, err := RecordPlanTo("shell", plan.NewMemoryStore(), "shell")
	if err != nil {
		t.Fatal(err)
	}
	if plan.VersionUserManageShell != 30 || ops[0].Version != 30 {
		t.Fatalf("version = %d, header = %d", plan.VersionUserManageShell, ops[0].Version)
	}
	encoded, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []plan.Op
	for _, line := range strings.Split(strings.TrimSpace(string(encoded)), "\n") {
		op, err := plan.DecodeOp([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, op)
	}
	current := "/bin/sh"
	var calls [][]string
	run := func(name string, args ...string) (string, string, int, error) {
		argv := append([]string{name}, args...)
		calls = append(calls, argv)
		switch {
		case name == "getent" && len(args) == 2 && args[0] == "passwd":
			shell := "/bin/sh"
			if args[1] == "gonfy" {
				shell = current
			}
			return args[1] + ":*:1001:1001::/home/" + args[1] + ":" + shell + "\n", "", 0, nil
		case reflect.DeepEqual(argv, []string{"usermod", "--shell", "/usr/bin/fish", "--", "gonfy"}):
			current = "/usr/bin/fish"
			return "", "", 0, nil
		default:
			t.Fatalf("unexpected command: %v", argv)
			return "", "", 1, nil
		}
	}
	ctx := runners.WithSet(context.Background(), &runners.Set{User: &runners.UserRunners{Run: run, GOOS: "linux"}})

	if err := plan.ApplyWithContext(ctx, decoded, plan.Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatal(err)
	}
	if !resource.AnyChanged("User[gonfy]") {
		t.Fatal("shell change not reported")
	}
	if current != "/usr/bin/fish" {
		t.Fatal("shell did not change")
	}
	resource.ResetReport()
	calls = nil
	if err := plan.ApplyWithContext(ctx, decoded, plan.Facts{GOOS: "linux"}, ""); err != nil {
		t.Fatal(err)
	}
	if resource.AnyChanged("User[gonfy]") || len(calls) != 3 {
		t.Fatalf("second apply: calls = %v", calls)
	}
}

func TestUserManageShellRecordingRefusal(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("invalid", "", func() { User("gonfy", WithManageShell) })
	if _, err := RecordPlanTo("invalid", plan.NewMemoryStore(), "invalid"); err == nil || !strings.Contains(err.Error(), "requires a shell") {
		t.Fatalf("record = %v", err)
	}
}
