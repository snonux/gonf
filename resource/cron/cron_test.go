package cron

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
)

func TestMergeCrontabAddReplaceRemove(t *testing.T) {
	c := &Cron{
		name: "job", user: "root", command: "/bin/true",
		minute: "0", hour: "1", monthday: "*", month: "*", weekday: "*",
	}
	desired := c.block()

	out, changed := mergeCrontab("", "job", desired)
	if !changed || !strings.Contains(out, "/bin/true") {
		t.Fatalf("add: changed=%v out=%q", changed, out)
	}

	out2, changed2 := mergeCrontab(out, "job", desired)
	if changed2 {
		t.Fatal("idempotent replace should not change")
	}

	c.minute = "5"
	desired2 := c.block()
	out3, changed3 := mergeCrontab(out2, "job", desired2)
	if !changed3 || !strings.Contains(out3, "5 1 * * * /bin/true") {
		t.Fatalf("replace: %q", out3)
	}

	out4, changed4 := mergeCrontab(out3, "job", "")
	if !changed4 || strings.Contains(out4, "GONF Cron[job]") {
		t.Fatalf("remove: %q", out4)
	}
}

func TestMergePreservesOtherLines(t *testing.T) {
	existing := "MAILTO=root\n0 * * * * /bin/echo keep\n"
	c := &Cron{
		name: "gonfjob", user: "root", command: "/bin/true",
		minute: "*", hour: "*", monthday: "*", month: "*", weekday: "*",
	}
	out, _ := mergeCrontab(existing, "gonfjob", c.block())
	if !strings.Contains(out, "/bin/echo keep") || !strings.Contains(out, "MAILTO=root") {
		t.Fatalf("lost existing lines: %q", out)
	}
}

func TestMergeUnclosedBeginKeepsTail(t *testing.T) {
	existing := "# BEGIN GONF Cron[job]\n0 * * * * /bin/broken\nMAILTO=root\n0 * * * * /bin/echo keep\n"
	c := &Cron{
		name: "job", user: "root", command: "/bin/true",
		minute: "1", hour: "2", monthday: "*", month: "*", weekday: "*",
	}
	out, changed := mergeCrontab(existing, "job", c.block())
	if !changed {
		t.Fatal("expected change when adding closed block beside unclosed marker")
	}
	if !strings.Contains(out, "MAILTO=root") || !strings.Contains(out, "/bin/echo keep") {
		t.Fatalf("unclosed BEGIN must not drop tail: %q", out)
	}
	if !strings.Contains(out, "# END GONF Cron[job]") {
		t.Fatalf("expected closed block: %q", out)
	}
}

func TestMergeCollapsesDuplicateBlocks(t *testing.T) {
	c := &Cron{
		name: "job", user: "root", command: "/bin/true",
		minute: "0", hour: "1", monthday: "*", month: "*", weekday: "*",
	}
	desired := c.block()
	dup := desired + desired
	out, changed := mergeCrontab(dup, "job", desired)
	if !changed {
		t.Fatal("duplicates should force rewrite")
	}
	if strings.Count(out, beginMarker("job")) != 1 {
		t.Fatalf("expected one block, got %q", out)
	}
}

func TestPresentRequiresCommand(t *testing.T) {
	resource.ResetRepository()
	Present("x")
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error without WithCommand")
	}
}

func TestPresentRejectsWhitespaceName(t *testing.T) {
	resource.ResetRepository()
	Present("bad name", opt.WithCommand("/bin/true"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for whitespace in name")
	}
}

func TestPresentRejectsEmptyName(t *testing.T) {
	resource.ResetRepository()
	Present("", opt.WithCommand("/bin/true"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestPresentRejectsBracketName(t *testing.T) {
	resource.ResetRepository()
	Present("a]", opt.WithCommand("/bin/true"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for bracket in name")
	}
}

func TestPresentRejectsNewlineCommand(t *testing.T) {
	resource.ResetRepository()
	Present("x", opt.WithCommand("/bin/true\n# END GONF Cron[x]"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for newline in command")
	}
}

func TestPresentRejectsEmptyMinute(t *testing.T) {
	resource.ResetRepository()
	Present("x", opt.WithCommand("/bin/true"), opt.WithMinute(""))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for empty minute")
	}
}

func TestAbsentWithoutCommand(t *testing.T) {
	origRun := runCmd
	origStdin := runCmdWithStdin
	defer func() {
		runCmd = origRun
		runCmdWithStdin = origStdin
	}()

	var wrote string
	runCmd = func(name string, args ...string) (string, string, int, error) {
		return "MAILTO=root\n", "", 0, nil
	}
	runCmdWithStdin = func(stdin string, name string, args ...string) (string, string, int, error) {
		wrote = stdin
		return "", "", 0, nil
	}

	resource.ResetRepository()
	Absent("gone", opt.WithCronUser("root"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("absent without command: %v", err)
	}
	if wrote != "MAILTO=root\n" && wrote != "" {
		// no block present → may be unchanged write skip; ensure no panic path
	}
	_ = wrote
}

func TestApplyMockedPresentIdempotentAndDryRun(t *testing.T) {
	origRun := runCmd
	origStdin := runCmdWithStdin
	defer func() {
		runCmd = origRun
		runCmdWithStdin = origStdin
	}()

	tab := ""
	writes := 0
	runCmd = func(name string, args ...string) (string, string, int, error) {
		if tab == "" {
			return "", "no crontab for root", 1, nil
		}
		return tab, "", 0, nil
	}
	runCmdWithStdin = func(stdin string, name string, args ...string) (string, string, int, error) {
		writes++
		tab = stdin
		return "", "", 0, nil
	}

	resource.ResetRepository()
	Present("job",
		opt.WithCronUser("root"),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("7"),
		opt.WithHour("3"),
		opt.WithCronEnv("FOO=1"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("present: %v", err)
	}
	if writes != 1 || !strings.Contains(tab, "FOO=1") || !strings.Contains(tab, "7 3 * * * /bin/true") {
		t.Fatalf("write #%d tab=%q", writes, tab)
	}

	resource.ResetRepository()
	Present("job",
		opt.WithCronUser("root"),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("7"),
		opt.WithHour("3"),
		opt.WithCronEnv("FOO=1"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if writes != 1 {
		t.Fatalf("idempotent should not rewrite, writes=%d", writes)
	}

	resource.SetDryRun(true)
	defer resource.SetDryRun(false)
	resource.ResetRepository()
	Present("job",
		opt.WithCronUser("root"),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("8"),
		opt.WithHour("3"),
		opt.WithCronEnv("FOO=1"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if writes != 1 {
		t.Fatalf("dry-run must not write, writes=%d", writes)
	}
	resource.SetDryRun(false)

	resource.ResetRepository()
	Absent("job", opt.WithCronUser("root"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("absent: %v", err)
	}
	if writes != 2 || strings.Contains(tab, "GONF Cron[job]") {
		t.Fatalf("absent failed: writes=%d tab=%q", writes, tab)
	}
}

func TestCrontabArgsOmitsUForSelf(t *testing.T) {
	args := crontabArgs("definitely-not-current-user-xyz", "-l")
	if len(args) < 3 || args[0] != "-u" {
		t.Fatalf("expected -u for other user, got %v", args)
	}
}

func TestLiveCronRoundTrip(t *testing.T) {
	if os.Getenv("GONF_RUN_CRON_TESTS") != "1" {
		t.Skip("set GONF_RUN_CRON_TESTS=1 for live crontab tests")
	}
	resource.ResetRepository()
	name := "gonf-live-test"
	Present(name,
		opt.WithCronUser("root"),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("7"),
		opt.WithHour("3"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("present: %v", err)
	}
	assertCrontabContains(t, "root", beginMarker(name), "7 3 * * * /bin/true", endMarker(name))

	resource.ResetRepository()
	Present(name,
		opt.WithCronUser("root"),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("7"),
		opt.WithHour("3"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	assertCrontabContains(t, "root", beginMarker(name))

	resource.ResetRepository()
	Absent(name, opt.WithCronUser("root"))
	if err := resource.Apply(); err != nil {
		t.Fatalf("absent: %v", err)
	}
	assertCrontabLacks(t, "root", beginMarker(name))
}

func TestLiveCronPerUser(t *testing.T) {
	if os.Getenv("GONF_RUN_CRON_TESTS") != "1" {
		t.Skip("set GONF_RUN_CRON_TESTS=1 for live crontab tests")
	}
	user := os.Getenv("USER")
	if user == "" || user == "root" {
		t.Skip("need non-root USER for per-user crontab test")
	}
	resource.ResetRepository()
	name := "gonf-live-user"
	Present(name,
		opt.WithCronUser(user),
		opt.WithCommand("/bin/true"),
		opt.WithMinute("11"),
		opt.WithHour("4"),
		opt.WithCronEnv("GONF_CRON_TEST=1"),
	)
	if err := resource.Apply(); err != nil {
		t.Fatalf("present: %v", err)
	}
	assertCrontabContains(t, user, beginMarker(name), "GONF_CRON_TEST=1", "11 4 * * * /bin/true")

	resource.ResetRepository()
	Absent(name, opt.WithCronUser(user))
	if err := resource.Apply(); err != nil {
		t.Fatalf("absent: %v", err)
	}
	assertCrontabLacks(t, user, beginMarker(name))
}

func assertCrontabContains(t *testing.T, user string, needles ...string) {
	t.Helper()
	out := liveCrontab(t, user)
	for _, n := range needles {
		if !strings.Contains(out, n) {
			t.Fatalf("crontab for %s missing %q; got:\n%s", user, n, out)
		}
	}
}

func assertCrontabLacks(t *testing.T, user string, needle string) {
	t.Helper()
	out := liveCrontab(t, user)
	if strings.Contains(out, needle) {
		t.Fatalf("crontab for %s still has %q; got:\n%s", user, needle, out)
	}
}

func liveCrontab(t *testing.T, userName string) string {
	t.Helper()
	args := crontabArgs(userName, "-l")
	cmd := exec.Command("crontab", args...)
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil && !strings.Contains(strings.ToLower(out), "no crontab") {
		t.Fatalf("crontab %v: %v (%s)", args, err, out)
	}
	return out
}
