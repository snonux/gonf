package cron

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"testing"
	"time"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/resource"
	"golang.org/x/sys/unix"
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

func TestAdoptLegacyCommandExactAndProtected(t *testing.T) {
	legacy := "/usr/local/bin/example --run >/dev/null 2>&1"
	current := strings.Join([]string{
		"MAILTO=root",
		"0 * * * * " + legacy,
		"0 * * * * " + legacy + " --extra",
		"# BEGIN GONF Cron[other]",
		"0 * * * * " + legacy,
		"# END GONF Cron[other]",
		"# a comment mentioning " + legacy,
		"",
	}, "\n")

	got, changed := adoptLegacyCommand(current, legacy)
	if !changed {
		t.Fatal("expected the unmanaged exact command to be adopted")
	}
	if strings.Count(got, legacy) != 3 {
		t.Fatalf("adoption removed a non-exact or protected line:\n%s", got)
	}
	if !strings.Contains(got, "MAILTO=root") || !strings.Contains(got, beginMarker("other")) {
		t.Fatalf("adoption lost unrelated crontab content:\n%s", got)
	}
}

func TestAdoptLegacyCommandFailsClosedOnMalformedMarkers(t *testing.T) {
	legacy := "/usr/local/bin/example"
	for _, current := range []string{
		"# BEGIN GONF Cron[broken]\n0 * * * * " + legacy + "\n",
		"# END GONF Cron[broken]\n0 * * * * " + legacy + "\n",
		"# BEGIN GONF something\n0 * * * * " + legacy + "\n",
		"# BEGIN GONF Cron[first]\n# BEGIN GONF Cron[second]\n0 * * * * " + legacy + "\n# END GONF Cron[second]\n# END GONF Cron[first]\n",
	} {
		got, changed := adoptLegacyCommand(current, legacy)
		if changed || got != current {
			t.Fatalf("malformed marker must disable adoption:\nwant %q\n got %q", current, got)
		}
	}
}

func TestAdoptLegacyCommandRejectsNonCronLines(t *testing.T) {
	legacy := "/usr/local/bin/example"
	for _, line := range []string{
		"# * * * * " + legacy,
		"@reboot " + legacy,
		"SHELL=/bin/sh * * * * " + legacy,
		"60 * * * * " + legacy,
		"* 24 * * * " + legacy,
		"* * 0 * * " + legacy,
		"* * * 13 * " + legacy,
		"* * * * 8 " + legacy,
		"*/0 * * * * " + legacy,
		"nonsense * * * * " + legacy,
	} {
		got, changed := adoptLegacyCommand(line+"\n", legacy)
		if changed || got != line+"\n" {
			t.Fatalf("non-cron line must not be adopted: %q", line)
		}
	}
}

func TestCronEntryCommandAcceptsPortableSyntax(t *testing.T) {
	command := "/usr/local/bin/example --run"
	for _, line := range []string{
		"0 * * * * " + command,
		"*/15 1-23/2 1,15 jan-mar mon-fri " + command,
	} {
		got, ok := cronEntryCommand(line)
		if !ok || got != command {
			t.Fatalf("cronEntryCommand(%q) = %q, %v", line, got, ok)
		}
	}
}

// TestCronEntryCommandSplitsOnlyOnCrontabBlanks pins the field separator to
// crontab(5)'s "blank" (ASCII space or tab). A byte-wise
// unicode.IsSpace(rune(b)) test used to treat the lone bytes 0x85 and 0xA0 —
// UTF-8 continuation bytes, not characters — as whitespace, and a rune-wise
// Unicode test would treat U+00A0/U+2003 as separators. cron itself does
// neither, so both would move the command boundary away from what cron runs.
func TestCronEntryCommandSplitsOnlyOnCrontabBlanks(t *testing.T) {
	testCases := []struct {
		name    string
		line    string
		command string
		ok      bool
	}{
		{"tabs separate fields", "0\t*\t*\t*\t*\t/bin/cmd", "/bin/cmd", true},
		{"mixed runs of blanks", "0 \t *  *\t\t* *  \t/bin/cmd", "/bin/cmd", true},
		{"multibyte command kept intact", "0 * * * * /bin/echo héllo à\u00a0x", "/bin/echo héllo à\u00a0x", true},
		{"no-break space starts the command", "0 * * * * \u00a0/bin/cmd", "\u00a0/bin/cmd", true},
		{"em space starts the command", "0 * * * * \u2003/bin/cmd", "\u2003/bin/cmd", true},
		{"lone 0xA0 byte starts the command", "0 * * * * \xa0/bin/cmd", "\xa0/bin/cmd", true},
		{"lone 0x85 byte starts the command", "0 * * * * \x85/bin/cmd", "\x85/bin/cmd", true},
		{"no-break space is not a field separator", "0\u00a0* * * * /bin/cmd", "", false},
		{"no-break space before the command", "0 * * * *\u00a0/bin/cmd", "", false},
		{"ideographic space is not a separator", "0\u3000* * * * * /bin/cmd", "", false},
		{"lone 0xA0 byte is not a separator", "0\xa0* * * * /bin/cmd", "", false},
		{"lone 0x85 byte is not a separator", "0 *\x85* * * /bin/cmd", "", false},
		{"vertical tab is not a separator", "0\v* * * * /bin/cmd", "", false},
		{"leading no-break space", "\u00a00 * * * * /bin/cmd", "", false},
		{"only blanks after fields", "0 * * * * \t ", "", false},
		{"leading blanks before a comment", " \t# 0 * * * * /bin/cmd", "", false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := cronEntryCommand(testCase.line)
			if ok != testCase.ok || got != testCase.command {
				t.Fatalf("cronEntryCommand(%q) = %q, %v; want %q, %v", testCase.line, got, ok, testCase.command, testCase.ok)
			}
		})
	}
}

// TestAdoptLegacyCommandIgnoresNonBlankWhitespace is the adoption-level
// consequence: a line whose command, as cron sees it, begins with a
// non-blank byte or rune is a different command and must never be deleted.
func TestAdoptLegacyCommandIgnoresNonBlankWhitespace(t *testing.T) {
	legacy := "/usr/local/bin/example"
	for _, line := range []string{
		"0 * * * * \xa0" + legacy,
		"0 * * * * \x85" + legacy,
		"0 * * * * \u00a0" + legacy,
		"0 * * * *\u00a0" + legacy,
		"0\u00a0* * * * " + legacy,
		"0 * * * * \u2003" + legacy,
	} {
		got, changed := adoptLegacyCommand(line+"\n", legacy)
		if changed || got != line+"\n" {
			t.Fatalf("line with non-blank whitespace was adopted: %q", line)
		}
	}
	if got, changed := adoptLegacyCommand("0\t*\t*\t*\t*\t"+legacy+"\n", legacy); !changed || got != "" {
		t.Fatalf("tab-separated legacy entry was not adopted: changed=%v got=%q", changed, got)
	}
}

func TestMergeUnclosedBeginKeepsTail(t *testing.T) {
	existing := "# BEGIN GONF Cron[job]\n0 * * * * /bin/broken\nMAILTO=root\n0 * * * * /bin/echo keep\n"
	c := &Cron{
		name: "job", user: "root", command: "/bin/true",
		minute: "1", hour: "2", monthday: "*", month: "*", weekday: "*",
	}
	desired := c.block()
	out, changed := mergeCrontab(existing, "job", desired)
	if !changed {
		t.Fatal("expected change when healing unclosed marker")
	}
	if !strings.Contains(out, "MAILTO=root") || !strings.Contains(out, "/bin/echo keep") {
		t.Fatalf("unclosed BEGIN must not drop tail: %q", out)
	}
	if strings.Count(out, beginMarker("job")) != 1 || !strings.Contains(out, endMarker("job")) {
		t.Fatalf("expected one closed block: %q", out)
	}
	out2, changed2 := mergeCrontab(out, "job", desired)
	if changed2 {
		t.Fatalf("healed crontab must be idempotent, got %q", out2)
	}

	// Absent clears unclosed marker alone.
	onlyBroken := "# BEGIN GONF Cron[job]\nMAILTO=root\n"
	out3, changed3 := mergeCrontab(onlyBroken, "job", "")
	if !changed3 || strings.Contains(out3, beginMarker("job")) || !strings.Contains(out3, "MAILTO=root") {
		t.Fatalf("absent should drop unclosed BEGIN: %q", out3)
	}
}

func TestPresentRejectsBadEnvAndBlankCommand(t *testing.T) {
	resource.ResetRepository()
	Present("x", opt.WithCommand("/bin/true"), opt.WithCronEnv("NOTANENV"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for env without =")
	}
	resource.ResetRepository()
	Present("x", opt.WithCommand("   "))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for blank command")
	}
	resource.ResetRepository()
	Present("x", opt.WithCommand("/bin/true"), opt.WithCronUser(""))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for empty cron user")
	}
	resource.ResetRepository()
	Present("x", opt.WithCommand("/bin/true"), opt.WithLegacyCommand("\n"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for invalid legacy command")
	}
	resource.ResetRepository()
	Absent("x", opt.WithLegacyCommand("/bin/true"))
	if err := resource.Apply(); err == nil {
		t.Fatal("expected error for legacy adoption on absent cron")
	}
	for _, option := range []opt.CronOption{
		opt.WithMinute("60"),
		opt.WithHour("24"),
		opt.WithMonthday("0"),
		opt.WithMonth("13"),
		opt.WithWeekday("8"),
		opt.WithMinute("@reboot"),
		opt.WithMinute("1/2"),
		opt.WithMinute("+1"),
		opt.WithMinute("*/+2"),
		opt.WithMinute("1-2/+2"),
		opt.WithMonth("jan/2"),
	} {
		resource.ResetRepository()
		Present("x", opt.WithCommand("/bin/true"), option)
		if err := resource.Apply(); err == nil {
			t.Fatal("expected error for invalid cron schedule")
		}
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
	Absent("gone", opt.WithCronUser(currentCronUser(t)))
	if err := resource.Apply(); err != nil {
		t.Fatalf("absent without command: %v", err)
	}
	// no block present → may be unchanged write skip; ensure no panic path
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

	userName := currentCronUser(t)
	resource.ResetRepository()
	Present("job",
		opt.WithCronUser(userName),
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
		opt.WithCronUser(userName),
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
		opt.WithCronUser(userName),
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
	Absent("job", opt.WithCronUser(userName))
	if err := resource.Apply(); err != nil {
		t.Fatalf("absent: %v", err)
	}
	if writes != 2 || strings.Contains(tab, "GONF Cron[job]") {
		t.Fatalf("absent failed: writes=%d tab=%q", writes, tab)
	}
}

func TestEnsureAdoptsLegacyCommandAndPreservesMixedCrontab(t *testing.T) {
	t.Cleanup(ResetRunnersForTest)
	legacy := "/usr/local/bin/old-job"
	tab := strings.Join([]string{
		"PATH=/usr/bin:/bin",
		"0 * * * * " + legacy,
		"# BEGIN GONF Cron[other]",
		"0 * * * * " + legacy,
		"# END GONF Cron[other]",
		"0 * * * * /usr/local/bin/keep",
		"",
	}, "\n")
	writes := 0
	SetRunnersForTest(
		func(name string, args ...string) (string, string, int, error) { return tab, "", 0, nil },
		func(stdin string, name string, args ...string) (string, string, int, error) {
			writes++
			tab = stdin
			return "", "", 0, nil
		},
	)

	userName := currentCronUser(t)
	if err := Ensure("new-job", opt.WithCronUser(userName), opt.WithCommand("/usr/local/bin/new-job"), opt.WithLegacyCommand(legacy)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if strings.Count(tab, legacy) != 1 || !strings.Contains(tab, beginMarker("new-job")) || !strings.Contains(tab, "/usr/local/bin/keep") {
		t.Fatalf("mixed crontab was not adopted safely:\n%s", tab)
	}
	if err := Ensure("new-job", opt.WithCronUser(userName), opt.WithCommand("/usr/local/bin/new-job"), opt.WithLegacyCommand(legacy)); err != nil {
		t.Fatalf("repeat apply: %v", err)
	}
	if writes != 1 {
		t.Fatalf("repeat apply rewrote converged crontab %d times", writes)
	}
}

func TestEnsureLegacyAdoptionReadFailureDoesNotWrite(t *testing.T) {
	t.Cleanup(ResetRunnersForTest)
	wrote := false
	SetRunnersForTest(
		func(name string, args ...string) (string, string, int, error) {
			return "", "permission denied", 1, nil
		},
		func(stdin string, name string, args ...string) (string, string, int, error) {
			wrote = true
			return "", "", 0, nil
		},
	)
	if err := Ensure("new-job", opt.WithCronUser(currentCronUser(t)), opt.WithCommand("/usr/local/bin/new-job"), opt.WithLegacyCommand("/usr/local/bin/old-job")); err == nil {
		t.Fatal("expected crontab probe failure")
	}
	if wrote {
		t.Fatal("a failed crontab probe must not be treated as an empty crontab")
	}
}

func TestEnsureConcurrentCronUpdatesDoNotLoseEitherBlock(t *testing.T) {
	t.Cleanup(ResetRunnersForTest)
	var tab string
	var tabMu sync.Mutex
	SetRunnersForTest(
		func(name string, args ...string) (string, string, int, error) {
			tabMu.Lock()
			defer tabMu.Unlock()
			return tab, "", 0, nil
		},
		func(stdin string, name string, args ...string) (string, string, int, error) {
			tabMu.Lock()
			defer tabMu.Unlock()
			tab = stdin
			return "", "", 0, nil
		},
	)

	userName := currentCronUser(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			errs <- Ensure(name, opt.WithCronUser(userName), opt.WithCommand("/usr/local/bin/"+name))
		}(name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent apply: %v", err)
		}
	}
	if !strings.Contains(tab, beginMarker("first")) || !strings.Contains(tab, beginMarker("second")) {
		t.Fatalf("concurrent applies lost a managed block:\n%s", tab)
	}
}

func TestCrontabLockSerializesSeparateProcesses(t *testing.T) {
	userName := currentCronUser(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrontabLockHelper$")
	cmd.Env = append(os.Environ(), "GONF_CRON_LOCK_HELPER=1", "GONF_CRON_LOCK_USER="+userName)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		_ = stdin.Close()
		_ = cmd.Wait()
		t.Fatalf("lock helper did not start: line=%q err=%v", line, err)
	}

	start := time.Now()
	if _, err := lockCrontabWithin(userName, 40*time.Millisecond); err == nil {
		t.Fatal("second process acquired lock before release")
	} else if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("contended lock did not time out promptly: %s", elapsed)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockCrontabWithin(userName, time.Second)
	if err != nil {
		t.Fatalf("second process lock after release: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestCrontabLockRejectsHostilePreseed(t *testing.T) {
	testCases := []struct {
		name string
		seed func(t *testing.T, dir, path string)
	}{
		{
			name: "world-readable directory",
			seed: func(t *testing.T, dir, _ string) {
				t.Helper()
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked directory",
			seed: func(t *testing.T, dir, _ string) {
				t.Helper()
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "world-readable lock file",
			seed: func(t *testing.T, dir, path string) {
				t.Helper()
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			targetUID := uint32(unix.Geteuid())
			dir, path := crontabLockPath(root, targetUID, "target")
			testCase.seed(t, dir, path)
			if _, err := lockCrontabAtUID("target", targetUID, 10*time.Millisecond, root); err == nil {
				t.Fatal("hostile preseed was accepted")
			}
		})
	}
}

func TestCrontabLockUsesTargetUIDNamespace(t *testing.T) {
	root := t.TempDir()
	targetUser := currentCronUser(t)
	targetUID, err := crontabUserID(targetUser)
	if err != nil {
		t.Fatal(err)
	}

	// The caller UID is deliberately absent from the path calculation. These
	// model root applying targetUser's crontab and targetUser applying it
	// directly: both resolve to this one target-owned namespace.
	rootApplyingDir, rootApplyingPath := crontabLockPath(root, targetUID, targetUser)
	targetApplyingDir, targetApplyingPath := crontabLockPath(root, targetUID, targetUser)
	if rootApplyingDir != targetApplyingDir || rootApplyingPath != targetApplyingPath {
		t.Fatalf("same target user chose different lock paths: root=%s/%s target=%s/%s", rootApplyingDir, rootApplyingPath, targetApplyingDir, targetApplyingPath)
	}

	otherUID := targetUID + 1
	otherDir, _ := crontabLockPath(root, otherUID, targetUser)
	if otherDir == targetApplyingDir {
		t.Fatalf("target UID did not scope lock directory: %q", otherDir)
	}
}

func TestCrontabLockPrivilegedCreationTransfersOwnership(t *testing.T) {
	if unix.Geteuid() != 0 {
		t.Skip("requires root to model a root apply for another account")
	}
	const targetUID = uint32(65534)
	root := t.TempDir()
	dir, path := crontabLockPath(root, targetUID, "target")

	unlock, err := lockCrontabAtUID("target", targetUID, time.Second, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlock(); err != nil {
			t.Error(err)
		}
	}()

	for _, candidate := range []struct {
		path string
		mode uint32
	}{
		{dir, unix.S_IFDIR | 0o700},
		{path, unix.S_IFREG | 0o600},
	} {
		var stat unix.Stat_t
		if err := unix.Stat(candidate.path, &stat); err != nil {
			t.Fatal(err)
		}
		if stat.Uid != targetUID || uint32(stat.Mode&unix.S_IFMT) != candidate.mode&unix.S_IFMT || uint32(stat.Mode&0o777) != candidate.mode&0o777 {
			t.Fatalf("lock object %s has uid=%d mode=%#o, want uid=%d mode=%#o", candidate.path, stat.Uid, stat.Mode, targetUID, candidate.mode)
		}
	}
}

func TestCrontabUserIDResolvesCurrentAccount(t *testing.T) {
	current := currentCronUser(t)
	uid, err := crontabUserID(current)
	if err != nil {
		t.Fatal(err)
	}
	if uid != uint32(unix.Geteuid()) {
		t.Fatalf("resolved uid %d, want effective uid %d", uid, unix.Geteuid())
	}
}

func currentCronUser(t *testing.T) string {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if current.Username == "" {
		t.Fatal("current user has no username")
	}
	return current.Username
}

func TestCronFieldRejectsNonPortableForms(t *testing.T) {
	minute := cronFieldRange("minute")
	for _, value := range []string{
		"1/2", "1/+2", "*/+2", "1-2/+2", "+1", "+1-2", "1-2/", "jan", "", "1,,2",
	} {
		if validCronField(value, minute) {
			t.Fatalf("minute field accepted invalid portable syntax %q", value)
		}
	}
	month := cronFieldRange("month")
	for _, value := range []string{"jan/2", "jan-mar/+2"} {
		if validCronField(value, month) {
			t.Fatalf("month field accepted invalid portable syntax %q", value)
		}
	}
	if !validCronField("*/15,1-23/2", minute) || !validCronField("jan-mar/2", month) {
		t.Fatal("valid portable stepped field rejected")
	}
}

func TestCrontabLockHelper(t *testing.T) {
	if os.Getenv("GONF_CRON_LOCK_HELPER") != "1" {
		return
	}
	unlock, err := lockCrontab(os.Getenv("GONF_CRON_LOCK_USER"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlock(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
		t.Fatal(err)
	}
	var byteRead [1]byte
	if _, err := os.Stdin.Read(byteRead[:]); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
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
