package user

import (
	"fmt"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// bsdBackend constructs the OpenBSD or NetBSD backend under test.
type bsdBackend func(Runner) Backend

func TestOpenBSDEnsureCommandMatrix(t *testing.T) {
	testBSDEnsureCommandMatrix(t, func(runner Runner) Backend {
		return NewOpenBSD(runner)
	})
}

func TestNetBSDEnsureCommandMatrix(t *testing.T) {
	testBSDEnsureCommandMatrix(t, func(runner Runner) Backend {
		return NewNetBSD(runner)
	})
}

func TestOpenBSDEnsureNoOp(t *testing.T) {
	testBSDEnsureNoOp(t, func(runner Runner) Backend {
		return NewOpenBSD(runner)
	})
}

func TestNetBSDEnsureNoOp(t *testing.T) {
	testBSDEnsureNoOp(t, func(runner Runner) Backend {
		return NewNetBSD(runner)
	})
}

// TestOpenBSDEnsureUpdate pins the OpenBSD argv: usermod -G receives only the
// missing group because OpenBSD documents -G as appending.
func TestOpenBSDEnsureUpdate(t *testing.T) {
	calls := bsdUpdateCalls(nil, scriptedCall{command: "usermod", args: []string{"-G", "audio", "svc"}})
	if err := ensureAs(NewOpenBSD(scriptedRunner(t, calls)), bsdUpdateWant()); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

// TestNetBSDEnsureUpdate pins the NetBSD argv: usermod -G receives every
// group that already lists the account (video, wheel) plus the missing one,
// so a replacing -G keeps the existing memberships. Groups whose member list
// only contains a longer name sharing the prefix, and the primary group that
// does not list the account, are not included.
func TestNetBSDEnsureUpdate(t *testing.T) {
	calls := bsdUpdateCalls(
		[]scriptedCall{{command: "getent", args: []string{"group"}, stdout: bsdGroupDB}},
		scriptedCall{command: "usermod", args: []string{"-G", "audio,video,wheel", "svc"}},
	)
	if err := ensureAs(NewNetBSD(scriptedRunner(t, calls)), bsdUpdateWant()); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

// bsdGroupDB is a getent group enumeration in which svc is an explicit member
// of wheel and video, has primary group svc, and is not in audio or staff
// (staff only lists svcx).
const bsdGroupDB = "wheel:*:0:root,svc\nsvc:*:1001:\nvideo:*:44:svc\naudio:*:45:\nstaff:*:20:root,svcx\n"

func bsdUpdateWant() DesiredUser {
	return DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel", "audio", "wheel"}}
}

// bsdUpdateCalls is the command sequence for adding the missing audio
// membership to an existing svc account: the account and membership probes
// (id -Gn, then any platform union probes) run before the missing audio
// group is created, and usermod runs last.
func bsdUpdateCalls(unionProbes []scriptedCall, usermod scriptedCall) []scriptedCall {
	calls := []scriptedCall{
		{command: "getent", args: []string{"passwd", "svc"}},
		{command: "id", args: []string{"-Gn", "svc"}, stdout: "svc wheel video\n"},
	}
	calls = append(calls, unionProbes...)
	return append(calls,
		scriptedCall{command: "getent", args: []string{"group", "audio"}, code: 2},
		scriptedCall{command: "groupadd", args: []string{"audio"}},
		scriptedCall{command: "getent", args: []string{"group", "wheel"}},
		usermod,
	)
}

func TestOpenBSDEnsureInvalidLoginClass(t *testing.T) {
	testBSDEnsureInvalidLoginClass(t, func(runner Runner) Backend {
		return NewOpenBSD(runner)
	})
}

func TestNetBSDEnsureInvalidLoginClass(t *testing.T) {
	testBSDEnsureInvalidLoginClass(t, func(runner Runner) Backend {
		return NewNetBSD(runner)
	})
}

func TestOpenBSDEnsureDryRun(t *testing.T) {
	testBSDEnsureDryRun(t, func(runner Runner) Backend {
		return NewOpenBSD(runner)
	})
}

func TestNetBSDEnsureDryRun(t *testing.T) {
	testBSDEnsureDryRun(t, func(runner Runner) Backend {
		return NewNetBSD(runner)
	})
}

func TestOpenBSDEnsureSupplementaryGroupLimit(t *testing.T) {
	testBSDEnsureSupplementaryGroupLimit(t, func(runner Runner) Backend {
		return NewOpenBSD(runner)
	}, nil)
}

func TestNetBSDEnsureSupplementaryGroupLimit(t *testing.T) {
	testBSDEnsureSupplementaryGroupLimit(t, func(runner Runner) Backend {
		return NewNetBSD(runner)
	}, []scriptedCall{{command: "getent", args: []string{"group"}, stdout: "svc:*:1001:\n"}})
}

func TestNetBSDEnsureSurfacesUnavailableLoginClassOption(t *testing.T) {
	err := ensureAs(NewNetBSD(scriptedRunner(t, []scriptedCall{
		{command: "getent", args: []string{"passwd", "svc"}, code: 2},
		{command: "useradd", args: []string{"-L", "daemon", "svc"}, code: 1, stderr: "illegal option -- L"},
	})), DesiredUser{Name: "svc", LoginClass: "daemon"})

	if err == nil || !strings.Contains(err.Error(), "useradd -L daemon svc failed (exit 1)") {
		t.Fatalf("Ensure() = %v", err)
	}
}

func TestBSDEnsureRejectsUnsupportedSystemAccount(t *testing.T) {
	for name, newBackend := range map[string]bsdBackend{
		"openbsd": func(runner Runner) Backend { return NewOpenBSD(runner) },
		"netbsd":  func(runner Runner) Backend { return NewNetBSD(runner) },
	} {
		t.Run(name, func(t *testing.T) {
			err := ensureAs(newBackend(scriptedRunner(t, []scriptedCall{
				{command: "getent", args: []string{"passwd", "svc"}, code: 2},
			})), DesiredUser{Name: "svc", System: true})

			if err == nil || !strings.Contains(err.Error(), "system accounts are not supported") {
				t.Fatalf("Ensure() = %v", err)
			}
		})
	}
}

func TestNewBSDBackendsUseDefaultRunner(t *testing.T) {
	for name, run := range map[string]Runner{
		"openbsd": NewOpenBSD(nil).run,
		"netbsd":  NewNetBSD(nil).run,
	} {
		t.Run(name, func(t *testing.T) {
			if got := fmt.Sprintf("%T", run); got == "<nil>" {
				t.Fatal("nil default runner")
			}
		})
	}
}

func testBSDEnsureCommandMatrix(t *testing.T, newBackend bsdBackend) {
	t.Helper()
	want := DesiredUser{
		Name:                "svc",
		PrimaryGroup:        "svc",
		SupplementaryGroups: []string{"wheel", "audio", "wheel"},
		Home:                "/var/lib/svc",
		CreateHome:          true,
		Shell:               "/sbin/nologin",
		LoginClass:          "daemon",
	}
	calls := []scriptedCall{
		{command: "getent", args: []string{"passwd", "svc"}, code: 2},
		{command: "getent", args: []string{"group", "audio"}, code: 2},
		{command: "groupadd", args: []string{"audio"}},
		{command: "getent", args: []string{"group", "svc"}, code: 2},
		{command: "groupadd", args: []string{"svc"}},
		{command: "getent", args: []string{"group", "wheel"}},
		{command: "useradd", args: []string{"-m", "-g", "svc", "-G", "audio,wheel", "-d", "/var/lib/svc", "-s", "/sbin/nologin", "-L", "daemon", "svc"}},
	}
	if err := ensureAs(newBackend(scriptedRunner(t, calls)), want); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

func testBSDEnsureNoOp(t *testing.T, newBackend bsdBackend) {
	t.Helper()
	calls := []scriptedCall{
		{command: "getent", args: []string{"passwd", "svc"}},
		{command: "id", args: []string{"-Gn", "svc"}, stdout: "svc audio wheel\n"},
		{command: "getent", args: []string{"group", "audio"}},
		{command: "getent", args: []string{"group", "wheel"}},
	}
	if err := ensureAs(newBackend(scriptedRunner(t, calls)), DesiredUser{
		Name:                "svc",
		PrimaryGroup:        "svc",
		SupplementaryGroups: []string{"wheel", "audio"},
		Home:                "/var/lib/svc",
		CreateHome:          true,
		Shell:               "/sbin/nologin",
		LoginClass:          "daemon",
		System:              true,
	}); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

func testBSDEnsureInvalidLoginClass(t *testing.T, newBackend bsdBackend) {
	t.Helper()
	err := ensureAs(newBackend(scriptedRunner(t, []scriptedCall{
		{command: "getent", args: []string{"passwd", "svc"}, code: 2},
		{command: "useradd", args: []string{"-L", "not-a-class", "svc"}, code: 1, stderr: "unknown login class"},
	})), DesiredUser{Name: "svc", LoginClass: "not-a-class"})

	if err == nil || !strings.Contains(err.Error(), "useradd -L not-a-class svc failed (exit 1)") {
		t.Fatalf("Ensure() = %v", err)
	}
}

func testBSDEnsureDryRun(t *testing.T, newBackend bsdBackend) {
	t.Helper()
	originalDryRun := resource.DryRun()
	resource.ResetReport()
	resource.SetDryRun(true)
	t.Cleanup(func() {
		resource.SetDryRun(originalDryRun)
		resource.ResetReport()
	})

	err := ensureAs(newBackend(scriptedRunner(t, []scriptedCall{
		{command: "getent", args: []string{"passwd", "svc"}, code: 2},
		{command: "getent", args: []string{"group", "svc"}, code: 2},
	})), DesiredUser{Name: "svc", PrimaryGroup: "svc", CreateHome: true})

	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	if !resource.AnyChanged("Group[svc]", "User[svc]") {
		t.Fatal("dry-run did not report the suppressed mutations")
	}
}

// testBSDEnsureSupplementaryGroupLimit checks the desired-set limit on both
// BSDs. unionProbe is the extra group-database probe NetBSD issues before a
// membership update (nil on OpenBSD).
func testBSDEnsureSupplementaryGroupLimit(t *testing.T, newBackend bsdBackend, unionProbe []scriptedCall) {
	t.Helper()
	groups := bsdGroupNames(maxBSDSupplementaryGroups)
	joined := strings.Join(groups, ",")

	t.Run("creation accepts the portable limit without truncation", func(t *testing.T) {
		calls := make([]scriptedCall, 0, len(groups)+2)
		calls = append(calls, scriptedCall{command: "getent", args: []string{"passwd", "svc"}, code: 2})
		for _, group := range groups {
			calls = append(calls, scriptedCall{command: "getent", args: []string{"group", group}})
		}
		calls = append(calls, scriptedCall{command: "useradd", args: []string{"-G", joined, "svc"}})
		if err := ensureAs(newBackend(scriptedRunner(t, calls)), DesiredUser{
			Name:                "svc",
			SupplementaryGroups: groups,
		}); err != nil {
			t.Fatalf("Ensure() = %v", err)
		}
	})

	t.Run("update accepts the portable limit without truncation", func(t *testing.T) {
		calls := make([]scriptedCall, 0, len(groups)+3)
		calls = append(calls, scriptedCall{command: "getent", args: []string{"passwd", "svc"}})
		calls = append(calls, scriptedCall{command: "id", args: []string{"-Gn", "svc"}, stdout: "svc\n"})
		calls = append(calls, unionProbe...)
		for _, group := range groups {
			calls = append(calls, scriptedCall{command: "getent", args: []string{"group", group}})
		}
		calls = append(calls, scriptedCall{command: "usermod", args: []string{"-G", joined, "svc"}})
		if err := ensureAs(newBackend(scriptedRunner(t, calls)), DesiredUser{
			Name:                "svc",
			SupplementaryGroups: groups,
		}); err != nil {
			t.Fatalf("Ensure() = %v", err)
		}
	})

	t.Run("rejects more than the portable limit before probing", func(t *testing.T) {
		err := ensureAs(newBackend(scriptedRunner(t, nil)), DesiredUser{
			Name:                "svc",
			SupplementaryGroups: bsdGroupNames(maxBSDSupplementaryGroups + 1),
		})

		if err == nil || !strings.Contains(err.Error(), "at most 16 supplementary groups") {
			t.Fatalf("Ensure() = %v", err)
		}
	})
}

func bsdGroupNames(count int) []string {
	groups := make([]string, count)
	for i := range groups {
		groups[i] = fmt.Sprintf("g%02d", i)
	}
	return groups
}
