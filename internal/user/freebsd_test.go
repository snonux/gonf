package user

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

func TestFreeBSDEnsureCommandMatrix(t *testing.T) {
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
		{command: "pw", args: []string{"usershow", "-n", "svc"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupshow", "-n", "audio"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupadd", "-n", "audio"}},
		{command: "pw", args: []string{"groupshow", "-n", "svc"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupadd", "-n", "svc"}},
		{command: "pw", args: []string{"groupshow", "-n", "wheel"}},
		{command: "pw", args: []string{"useradd", "-n", "svc", "-m", "-g", "svc", "-G", "audio,wheel", "-d", "/var/lib/svc", "-s", "/sbin/nologin", "-L", "daemon"}},
	}
	if err := ensureAs(NewFreeBSD(scriptedRunner(t, calls)), want); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

func TestFreeBSDEnsureCreatesPrivateGroupByDefault(t *testing.T) {
	calls := []scriptedCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupshow", "-n", "svc"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupadd", "-n", "svc"}},
		{command: "pw", args: []string{"useradd", "-n", "svc", "-g", "svc"}},
	}
	if err := ensureAs(NewFreeBSD(scriptedRunner(t, calls)), DesiredUser{Name: "svc"}); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

func TestFreeBSDEnsureNoOp(t *testing.T) {
	calls := []scriptedCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, stdout: "svc:*:1001:1001::0:0::/var/lib/svc:/sbin/nologin\n"},
		{command: "pw", args: []string{"groupshow", "-a"}, stdout: "svc:*:1001:\naudio:*:1002:svc\nwheel:*:1003:svc\n"},
		{command: "pw", args: []string{"groupshow", "-n", "audio"}},
		{command: "pw", args: []string{"groupshow", "-n", "wheel"}},
	}
	if err := ensureAs(NewFreeBSD(scriptedRunner(t, calls)), DesiredUser{
		Name:                "svc",
		PrimaryGroup:        "different-primary-is-creation-only",
		SupplementaryGroups: []string{"wheel", "audio"},
		Home:                "/elsewhere",
		CreateHome:          true,
		Shell:               "/bin/sh",
		LoginClass:          "staff",
		System:              true,
	}); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

func TestFreeBSDEnsureUpdatePreservesExistingSecondaryGroups(t *testing.T) {
	existing := bsdGroupNames(17)
	var groupDatabase strings.Builder
	groupDatabase.WriteString("svc:*:1001:\n")
	for i, group := range existing {
		fmt.Fprintf(&groupDatabase, "%s:*:%d:svc\n", group, i+1002)
	}
	wantGroups := append(append([]string(nil), existing...), "audio", "wheel")
	sort.Strings(wantGroups)
	calls := []scriptedCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, stdout: "svc:*:1001:1001::0:0::/var/empty:/sbin/nologin\n"},
		{command: "pw", args: []string{"groupshow", "-a"}, stdout: groupDatabase.String()},
		{command: "pw", args: []string{"groupshow", "-n", "audio"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupadd", "-n", "audio"}},
		{command: "pw", args: []string{"groupshow", "-n", "wheel"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupadd", "-n", "wheel"}},
		{command: "pw", args: []string{"usermod", "-n", "svc", "-G", strings.Join(wantGroups, ",")}},
	}
	if err := ensureAs(NewFreeBSD(scriptedRunner(t, calls)), DesiredUser{
		Name:                "svc",
		SupplementaryGroups: []string{"wheel", "audio", "wheel"},
	}); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

// TestFreeBSDEnsureSkipsTheRealPrimaryGroupListedAsSupplementary pins that a
// requested supplementary group which is the account's real primary group
// (by gid) is neither probed nor created nor passed to pw usermod -G, which
// must exclude the primary group.
func TestFreeBSDEnsureSkipsTheRealPrimaryGroupListedAsSupplementary(t *testing.T) {
	calls := []scriptedCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, stdout: "svc:*:1001:1001::0:0::/var/empty:/sbin/nologin\n"},
		{command: "pw", args: []string{"groupshow", "-a"}, stdout: "svc:*:1001:\naudio:*:1002:\n"},
		{command: "pw", args: []string{"groupshow", "-n", "audio"}},
		{command: "pw", args: []string{"usermod", "-n", "svc", "-G", "audio"}},
	}
	if err := ensureAs(NewFreeBSD(scriptedRunner(t, calls)), DesiredUser{
		Name:                "svc",
		SupplementaryGroups: []string{"svc", "audio"},
	}); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

func TestFreeBSDEnsureInvalidStateStopsBeforeMutation(t *testing.T) {
	tests := []struct {
		name  string
		calls []scriptedCall
		match string
	}{
		{
			name:  "usershow reports unexpected exit",
			calls: []scriptedCall{{command: "pw", args: []string{"usershow", "-n", "svc"}, code: 70, stderr: "database unavailable"}},
			match: "pw usershow -n svc failed (exit 70)",
		},
		{
			name:  "usershow emits malformed record",
			calls: []scriptedCall{{command: "pw", args: []string{"usershow", "-n", "svc"}, stdout: "broken\n"}},
			match: "returned malformed passwd entry",
		},
		{
			name: "group lookup start failure",
			calls: []scriptedCall{
				{command: "pw", args: []string{"usershow", "-n", "svc"}, code: freeBSDNoUserExit},
				{command: "pw", args: []string{"groupshow", "-n", "svc"}},
				{command: "pw", args: []string{"groupshow", "-n", "wheel"}, err: errors.New("unavailable")},
			},
			match: "pw groupshow -n wheel",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureAs(NewFreeBSD(scriptedRunner(t, tt.calls)), DesiredUser{Name: "svc", SupplementaryGroups: []string{"wheel"}})
			if err == nil || !strings.Contains(err.Error(), tt.match) {
				t.Fatalf("Ensure() = %v, want %q", err, tt.match)
			}
		})
	}
}

func TestFreeBSDEnsureRejectsUnsafeRequestedHomeBeforeProbing(t *testing.T) {
	for _, home := range []string{"relative", "/", "//"} {
		t.Run(home, func(t *testing.T) {
			err := ensureAs(NewFreeBSD(scriptedRunner(t, nil)), DesiredUser{
				Name:       "svc",
				Home:       home,
				CreateHome: true,
			})

			if err == nil || !strings.Contains(err.Error(), "home must") {
				t.Fatalf("Ensure() = %v", err)
			}
		})
	}
}

func TestFreeBSDEnsureRejectsUnsupportedSystemAccount(t *testing.T) {
	err := ensureAs(NewFreeBSD(scriptedRunner(t, []scriptedCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, code: freeBSDNoUserExit},
	})), DesiredUser{Name: "svc", System: true})

	if err == nil || !strings.Contains(err.Error(), "system accounts are not supported") {
		t.Fatalf("Ensure() = %v", err)
	}
}

func TestFreeBSDEnsureDryRunProbesWithoutMutating(t *testing.T) {
	originalDryRun := resource.DryRun()
	resource.ResetReport()
	resource.SetDryRun(true)
	t.Cleanup(func() {
		resource.SetDryRun(originalDryRun)
		resource.ResetReport()
	})

	calls := []scriptedCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupshow", "-n", "svc"}, code: freeBSDNoUserExit},
	}
	err := ensureAs(NewFreeBSD(scriptedRunner(t, calls)), DesiredUser{
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

func TestNewFreeBSDUsesDefaultRunner(t *testing.T) {
	if got := fmt.Sprintf("%T", NewFreeBSD(nil).run); got == "<nil>" {
		t.Fatal("NewFreeBSD(nil) left runner nil")
	}
}

// TestFreeBSDGroupshowUnknownGroupIsMissing pins real pw(8) behavior:
// groupshow reports a missing group with EX_DATAERR and "unknown group", not
// EX_NOUSER, so the group must be created rather than failing the apply.
func TestFreeBSDGroupshowUnknownGroupIsMissing(t *testing.T) {
	calls := []scriptedCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupshow", "-n", "svc"}, code: freeBSDDataErrExit, stderr: "pw: unknown group `svc'\n"},
		{command: "pw", args: []string{"groupadd", "-n", "svc"}},
		{command: "pw", args: []string{"useradd", "-n", "svc", "-g", "svc"}},
	}
	if err := ensureAs(NewFreeBSD(scriptedRunner(t, calls)), DesiredUser{Name: "svc"}); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
}

// TestFreeBSDGroupshowOtherDataErrorFails keeps every other EX_DATAERR an
// error: only the "unknown group" message means the group is missing.
func TestFreeBSDGroupshowOtherDataErrorFails(t *testing.T) {
	calls := []scriptedCall{
		{command: "pw", args: []string{"usershow", "-n", "svc"}, code: freeBSDNoUserExit},
		{command: "pw", args: []string{"groupshow", "-n", "svc"}, code: freeBSDDataErrExit, stderr: "pw: group database corrupt\n"},
	}
	if err := ensureAs(NewFreeBSD(scriptedRunner(t, calls)), DesiredUser{Name: "svc"}); err == nil {
		t.Fatal("Ensure() = nil, want the groupshow data error")
	}
}
