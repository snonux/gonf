package timer

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

func TestNormalizeUnit(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fstrim", "fstrim.timer"},
		{"fstrim.timer", "fstrim.timer"},
		{"  logrotate  ", "logrotate.timer"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeUnit(c.in); got != c.want {
			t.Errorf("normalizeUnit(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateRejectsBadNames(t *testing.T) {
	bad := []*Timer{
		{name: ""},
		{name: ".timer"},
		{name: "foo/bar.timer"},
		{name: "foo bar.timer"},
	}
	for _, tm := range bad {
		if err := tm.validate(); err == nil {
			t.Errorf("expected validate error for name %q", tm.name)
		}
	}
	ok := &Timer{name: "fstrim.timer"}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPresentRejectsEmptyName(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	Present("")
	if err := resource.Apply(); err == nil {
		t.Fatal("expected empty name error")
	}
}

func TestPresentIdempotentWithFakeRunner(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	old := runCmd
	defer func() { runCmd = old }()
	runCmd = fakeSystemdAlreadyOK

	Present("fstrim")
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
}

func TestPresentEnablesAndStartsWhenInactive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	old := runCmd
	defer func() { runCmd = old }()

	var saw []string
	runCmd = func(name string, args ...string) (string, string, int, error) {
		if name != "systemctl" {
			return "", "", 1, nil
		}
		joined := join(args)
		if contains(args, "is-active") || contains(args, "is-enabled") {
			return "", "", 1, nil // inactive / disabled
		}
		if contains(args, "enable") || contains(args, "start") {
			saw = append(saw, joined)
			return "", "", 0, nil
		}
		return "", "unexpected " + joined, 1, nil
	}

	Present("fstrim.timer")
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if !containsStr(saw, "enable") || !containsStr(saw, "start") {
		t.Fatalf("expected enable+start, got %v", saw)
	}
}

func TestAbsentStopsAndDisables(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	old := runCmd
	defer func() { runCmd = old }()

	var saw []string
	runCmd = func(name string, args ...string) (string, string, int, error) {
		if name != "systemctl" {
			return "", "", 1, nil
		}
		joined := join(args)
		if contains(args, "is-active") || contains(args, "is-enabled") {
			return "", "", 0, nil
		}
		if contains(args, "stop") || contains(args, "disable") {
			saw = append(saw, joined)
			return "", "", 0, nil
		}
		return "", "unexpected " + joined, 1, nil
	}

	Absent("fstrim")
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if !containsStr(saw, "stop") || !containsStr(saw, "disable") {
		t.Fatalf("expected stop+disable, got %v", saw)
	}
}

func TestWithRestartIssuesRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	old := runCmd
	defer func() { runCmd = old }()

	var sawRestart bool
	runCmd = func(name string, args ...string) (string, string, int, error) {
		if contains(args, "restart") {
			sawRestart = true
		}
		return fakeSystemdAlreadyOK(name, args...)
	}

	Present("fstrim", opt.WithRestart)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if !sawRestart {
		t.Fatal("expected restart action")
	}
}

func TestWithUserPassesUserFlag(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	old := runCmd
	defer func() { runCmd = old }()

	var sawUser bool
	runCmd = func(name string, args ...string) (string, string, int, error) {
		if contains(args, "--user") {
			sawUser = true
		}
		return fakeSystemdAlreadyOK(name, args...)
	}

	Present("myjob", opt.WithUser)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if !sawUser {
		t.Fatal("expected --user")
	}
}

func TestPresentFailsWhenSystemctlMutateErrors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	old := runCmd
	defer func() { runCmd = old }()

	runCmd = func(name string, args ...string) (string, string, int, error) {
		if contains(args, "is-active") || contains(args, "is-enabled") {
			return "", "", 1, nil
		}
		if contains(args, "enable") {
			return "", "Permission denied", 1, nil
		}
		return "", "unexpected", 1, nil
	}

	Present("fstrim")
	if err := resource.Apply(); err == nil {
		t.Fatal("expected apply error when enable fails")
	}
}

func TestDryRunSkipsMutations(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	resource.ResetRepository()
	resource.SetDryRun(true)
	defer resource.SetDryRun(false)

	old := runCmd
	defer func() { runCmd = old }()

	var mutated bool
	runCmd = func(name string, args ...string) (string, string, int, error) {
		if contains(args, "enable") || contains(args, "start") {
			mutated = true
		}
		if contains(args, "is-active") || contains(args, "is-enabled") {
			return "", "", 1, nil
		}
		return "", "", 0, nil
	}

	Present("fstrim")
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("dry-run must not mutate")
	}
}

func TestRequireSystemdRejectsNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("only meaningful off Linux")
	}
	if err := requireSystemd(); err == nil {
		t.Fatal("expected non-Linux error")
	}
}

func fakeSystemdAlreadyOK(name string, args ...string) (string, string, int, error) {
	if name != "systemctl" {
		return "", "", 1, nil
	}
	if contains(args, "is-active") || contains(args, "is-enabled") {
		return "", "", 0, nil
	}
	if contains(args, "restart") {
		return "", "", 0, nil
	}
	return "", "unexpected systemctl " + join(args), 1, nil
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func join(args []string) string {
	return strings.Join(args, " ")
}

// Live tests (Fedora/systemd). Create disposable units, then Present/Absent.
//
//	# user timer (no sudo)
//	env GONF_RUN_TIMER_TESTS=1 go test ./resource/timer/ -run LiveUserTimer -v
//
//	# system timer (needs root)
//	sudo env GONF_RUN_TIMER_TESTS=1 go test ./resource/timer/ -run LiveSystemTimer -v

const liveUnit = "gonf-timer-live"

func TestLiveUserTimerRoundTrip(t *testing.T) {
	if os.Getenv("GONF_RUN_TIMER_TESTS") != "1" {
		t.Skip("set GONF_RUN_TIMER_TESTS=1 for live timer tests")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if u.Uid == "0" {
		t.Skip("LiveUserTimer must run as non-root")
	}

	unitDir := filepath.Join(u.HomeDir, ".config/systemd/user")
	installLiveUnits(t, unitDir)
	defer removeLiveUnits(t, unitDir, true)
	ctlLive(t, true, "daemon-reload")
	defer ctlLive(t, true, "disable", "--now", liveUnit+".timer")

	resource.ResetRepository()
	Present(liveUnit, opt.WithUser)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	assertLiveState(t, true, liveUnit+".timer", true, true)

	resource.ResetRepository()
	Present(liveUnit, opt.WithUser) // idempotent
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}

	resource.ResetRepository()
	Absent(liveUnit, opt.WithUser)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	assertLiveState(t, true, liveUnit+".timer", false, false)
}

func TestLiveSystemTimerRoundTrip(t *testing.T) {
	if os.Getenv("GONF_RUN_TIMER_TESTS") != "1" {
		t.Skip("set GONF_RUN_TIMER_TESTS=1 for live timer tests")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Timer is Linux-only")
	}
	if os.Geteuid() != 0 {
		t.Skip("LiveSystemTimer requires root (sudo)")
	}

	unitDir := "/run/systemd/system"
	installLiveUnits(t, unitDir)
	defer removeLiveUnits(t, unitDir, false)
	ctlLive(t, false, "daemon-reload")
	defer ctlLive(t, false, "disable", "--now", liveUnit+".timer")

	resource.ResetRepository()
	Present(liveUnit)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	assertLiveState(t, false, liveUnit+".timer", true, true)

	resource.ResetRepository()
	Absent(liveUnit)
	if err := resource.Apply(); err != nil {
		t.Fatal(err)
	}
	assertLiveState(t, false, liveUnit+".timer", false, false)
}

func installLiveUnits(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	service := `[Unit]
Description=gonf live timer test service

[Service]
Type=oneshot
ExecStart=/bin/true
`
	timer := `[Unit]
Description=gonf live timer test

[Timer]
OnCalendar=yearly
Persistent=false

[Install]
WantedBy=timers.target
`
	if err := os.WriteFile(filepath.Join(dir, liveUnit+".service"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, liveUnit+".timer"), []byte(timer), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeLiveUnits(t *testing.T, dir string, userBus bool) {
	t.Helper()
	_ = os.Remove(filepath.Join(dir, liveUnit+".service"))
	_ = os.Remove(filepath.Join(dir, liveUnit+".timer"))
	ctlLive(t, userBus, "daemon-reload")
}

func ctlLive(t *testing.T, userBus bool, args ...string) {
	t.Helper()
	full := args
	if userBus {
		full = append([]string{"--user"}, args...)
	}
	stdout, stderr, code, err := runCmd("systemctl", full...)
	if err != nil {
		t.Fatalf("systemctl %v: %v", full, err)
	}
	if code != 0 {
		t.Fatalf("systemctl %v exit %d: %s%s", full, code, stdout, stderr)
	}
}

func assertLiveState(t *testing.T, userBus bool, unit string, wantActive, wantEnabled bool) {
	t.Helper()
	active, err := isActive(unit, userBus)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := isEnabled(unit, userBus)
	if err != nil {
		t.Fatal(err)
	}
	if active != wantActive || enabled != wantEnabled {
		t.Fatalf("%s active=%v enabled=%v, want active=%v enabled=%v",
			unit, active, enabled, wantActive, wantEnabled)
	}
}
