package cron

import (
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
)

// identical is the default adoption of a job "10 6 * * * /usr/local/bin/x".
var identical = adoption{identical: &cronEntry{
	fields:  [5]string{"10", "6", "*", "*", "*"},
	command: "/usr/local/bin/x",
}}

// TestAdoptIdenticalOnlyTakesTheSameJob pins what "identical" means: the
// same five schedule fields (blank-insensitive, but not value-normalized)
// and exactly the same command, outside every Gonf block. A line with
// another schedule or command, a comment and a line inside another job's
// block are all kept.
func TestAdoptIdenticalOnlyTakesTheSameJob(t *testing.T) {
	current := strings.Join([]string{
		"MAILTO=root",
		"10 6 * * * /usr/local/bin/x",
		"10\t6  * * *  /usr/local/bin/x",
		"11 6 * * * /usr/local/bin/x",
		"010 6 * * * /usr/local/bin/x",
		"10 6 * * * /usr/local/bin/x --other",
		"10 6 * * * /usr/local/bin/x ",
		"# 10 6 * * * /usr/local/bin/x",
		"# BEGIN GONF Cron[other]",
		"10 6 * * * /usr/local/bin/x",
		"# END GONF Cron[other]",
		"",
	}, "\n")
	want := strings.Join([]string{
		"MAILTO=root",
		"11 6 * * * /usr/local/bin/x",
		"010 6 * * * /usr/local/bin/x",
		"10 6 * * * /usr/local/bin/x --other",
		"10 6 * * * /usr/local/bin/x ",
		"# 10 6 * * * /usr/local/bin/x",
		"# BEGIN GONF Cron[other]",
		"10 6 * * * /usr/local/bin/x",
		"# END GONF Cron[other]",
		"",
	}, "\n")
	got, changed := adoptUnmanaged(current, identical)
	if !changed || got != want {
		t.Fatalf("identical adoption:\n got %q\nwant %q", got, want)
	}
}

// TestAdoptIdenticalKeepsTheEnvironment pins that an identical entry with an
// environment assignment after it is kept: the managed block is appended at
// the end of the table, where the later assignment would apply to it. A
// legacy (WithLegacyCommand) match is explicit and still adopts it.
func TestAdoptIdenticalKeepsTheEnvironment(t *testing.T) {
	for _, current := range []string{
		"10 6 * * * /usr/local/bin/x\nPATH=/opt/bin\n",
		"10 6 * * * /usr/local/bin/x\nSHELL = /bin/ksh\n",
		"10 6 * * * /usr/local/bin/x\n# BEGIN GONF Cron[e]\nFOO=1\n0 * * * * /bin/e\n# END GONF Cron[e]\n",
	} {
		if got, changed := adoptUnmanaged(current, identical); changed || got != current {
			t.Fatalf("identical entry before an env line must stay:\n%q\n got %q", current, got)
		}
		legacy := adoption{legacy: "/usr/local/bin/x", identical: identical.identical}
		if _, changed := adoptUnmanaged(current, legacy); !changed {
			t.Fatalf("explicit WithLegacyCommand must still adopt:\n%q", current)
		}
	}
	after := "PATH=/opt/bin\n10 6 * * * /usr/local/bin/x\n0 * * * * /bin/keep\n"
	if got, changed := adoptUnmanaged(after, identical); !changed || got != "PATH=/opt/bin\n0 * * * * /bin/keep\n" {
		t.Fatalf("identical entry after the env line must be adopted, got %q", got)
	}
}

// TestCronAdoptionSelection pins which jobs adopt identical lines by
// default: a present job without WithCronEnv. A job with WithCronEnv runs
// with extra variables (not the same job), and an absent job never adopts.
func TestCronAdoptionSelection(t *testing.T) {
	plain := newCron("j", []opt.CronOption{opt.WithSchedule("10 6 * * *"), opt.WithCommand("/usr/local/bin/x")})
	if a := plain.adoption(); a.identical == nil || *a.identical != *identical.identical || a.legacy != "" {
		t.Fatalf("plain job adoption = %+v", a)
	}
	withEnv := newCron("j", []opt.CronOption{opt.WithCommand("/usr/local/bin/x"), opt.WithCronEnv("A=1")})
	if a := withEnv.adoption(); a.identical != nil {
		t.Fatalf("a job with WithCronEnv must not adopt identical lines: %+v", a)
	}
	absent := newCron("j", []opt.CronOption{opt.WithCommand("/usr/local/bin/x"), opt.IsAbsent})
	if a := absent.adoption(); a.identical != nil || a.legacy != "" {
		t.Fatalf("an absent job must adopt nothing: %+v", a)
	}
}

// TestEnsureAdoptsIdenticalLineByDefault converges a job over a crontab
// that already runs it unmanaged: the line moves into the Gonf block
// (instead of running twice), the rest of the table is kept, and a second
// apply is a no-op.
func TestEnsureAdoptsIdenticalLineByDefault(t *testing.T) {
	tab := "MAILTO=root\n*/5 8-22 * * * /usr/local/bin/gogios >/dev/null 2>&1\n0 3 * * * /usr/local/bin/keep\n"
	writes := 0
	cr := &runners.CronRunners{
		Read: func(string, ...string) (string, string, int, error) { return tab, "", 0, nil },
		Write: func(stdin string, _ string, _ ...string) (string, string, int, error) {
			writes++
			tab = stdin
			return "", "", 0, nil
		},
	}
	opts := []opt.CronOption{opt.WithCronUser(currentCronUser(t)),
		opt.WithSchedule("*/5 8-22 * * *"), opt.WithCommand("/usr/local/bin/gogios >/dev/null 2>&1")}
	for range 2 {
		if err := EnsureWith(cr, "gogios-checks", opts...); err != nil {
			t.Fatalf("EnsureWith: %v", err)
		}
	}
	want := "MAILTO=root\n0 3 * * * /usr/local/bin/keep\n" +
		"# BEGIN GONF Cron[gogios-checks]\n*/5 8-22 * * * /usr/local/bin/gogios >/dev/null 2>&1\n# END GONF Cron[gogios-checks]\n"
	if tab != want || writes != 1 {
		t.Fatalf("writes=%d tab:\n%s\nwant:\n%s", writes, tab, want)
	}
}

// TestSetScheduleSetsAllFields pins WithSchedule against the per-field
// options, including a later per-field override, and its misuse report.
func TestSetScheduleSetsAllFields(t *testing.T) {
	c := newCron("j", []opt.CronOption{opt.WithSchedule("0 2 1 jan mon"), opt.WithHour("3")})
	if got := [5]string{c.minute, c.hour, c.monthday, c.month, c.weekday}; got != [5]string{"0", "3", "1", "jan", "mon"} {
		t.Fatalf("fields = %v", got)
	}
	bad := newCron("j", []opt.CronOption{opt.WithSchedule("0 2 * *")})
	if err := bad.MisuseErr(); err == nil || !strings.Contains(err.Error(), "WithSchedule: schedule \"0 2 * *\" must have 5 fields") {
		t.Fatalf("misuse = %v", err)
	}
	if bad.minute != "*" {
		t.Fatalf("a refused schedule must leave the fields alone, minute = %q", bad.minute)
	}
}
