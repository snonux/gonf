package cron

import (
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
)

// identical is the default adoption of a job x, "10 6 * * * /usr/local/bin/x".
var identical = adoption{
	identical: &cronEntry{
		fields:  [5]string{"10", "6", "*", "*", "*"},
		command: "/usr/local/bin/x",
	},
	name:  "x",
	block: xBlock,
}

// xBlock is job x's managed block.
const xBlock = "# BEGIN GONF Cron[x]\n10 6 * * * /usr/local/bin/x\n# END GONF Cron[x]\n"

// TestAdoptIdenticalOnlyTakesTheSameJob pins what "identical" means: the
// same five schedule fields (blank-insensitive, but not value-normalized)
// and exactly the same command, outside every Gonf block. A line with
// another schedule or command, a comment and a line inside another job's
// block are all kept. The first identical line becomes the job's block in
// place; the second is a duplicate run and is removed.
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
		"# BEGIN GONF Cron[x]",
		"10 6 * * * /usr/local/bin/x",
		"# END GONF Cron[x]",
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

// TestAdoptIdenticalKeepsTheEnvironment pins that an identical entry
// followed by environment assignments (a plain NAME=value line, a spaced
// one, or another Gonf block's WithCronEnv) is adopted by replacing it with
// the job's block in place: the job keeps the environment it ran with and
// no longer runs twice. An explicit WithLegacyCommand for the same command
// takes the same in-place route.
func TestAdoptIdenticalKeepsTheEnvironment(t *testing.T) {
	for _, tail := range []string{
		"PATH=/opt/bin\n",
		"SHELL = /bin/ksh\n",
		"# BEGIN GONF Cron[e]\nFOO=1\n0 * * * * /bin/e\n# END GONF Cron[e]\n",
	} {
		current := "10 6 * * * /usr/local/bin/x\n" + tail
		want := xBlock + tail
		if got, changed := adoptUnmanaged(current, identical); !changed || got != want {
			t.Fatalf("identical entry before an env line:\n%q\n got %q\nwant %q", current, got, want)
		}
		legacy := identical
		legacy.legacy = "/usr/local/bin/x"
		if got, changed := adoptUnmanaged(current, legacy); !changed || got != want {
			t.Fatalf("WithLegacyCommand of the own command:\n%q\n got %q\nwant %q", current, got, want)
		}
	}
	after := "PATH=/opt/bin\n10 6 * * * /usr/local/bin/x\n0 * * * * /bin/keep\n"
	if got, changed := adoptUnmanaged(after, identical); !changed || got != "PATH=/opt/bin\n"+xBlock+"0 * * * * /bin/keep\n" {
		t.Fatalf("identical entry after the env line must become the block in place, got %q", got)
	}
}

// TestAdoptIdenticalDropsDuplicatesOfAnExistingBlock pins the state a
// version without in-place adoption left behind: the job's block already
// exists (at the end, after an env line) and the old unmanaged line still
// runs it a second time. The line is removed, the block stays where it is.
// A legacy-only adoption (a job with WithCronEnv) never places a block.
func TestAdoptIdenticalDropsDuplicatesOfAnExistingBlock(t *testing.T) {
	current := "10 6 * * * /usr/local/bin/x\nPATH=/opt/bin\n" + xBlock
	if got, changed := adoptUnmanaged(current, identical); !changed || got != "PATH=/opt/bin\n"+xBlock {
		t.Fatalf("duplicate of an existing block must go, got %q", got)
	}
	if got, changed := adoptUnmanaged("PATH=/opt/bin\n"+xBlock, identical); changed || got != "PATH=/opt/bin\n"+xBlock {
		t.Fatalf("a table without duplicates must stay unchanged, got %q", got)
	}
	legacyOnly := adoption{legacy: "/usr/local/bin/x"}
	if got, changed := adoptUnmanaged("10 6 * * * /usr/local/bin/x\nPATH=/opt/bin\n", legacyOnly); !changed || got != "PATH=/opt/bin\n" {
		t.Fatalf("legacy adoption removes the line and places nothing, got %q", got)
	}
}

// TestAdoptIdenticalRefusesMalformedMarkers is the negative case: a
// malformed Gonf marker disables adoption, so the identical line stays and
// no block is placed.
func TestAdoptIdenticalRefusesMalformedMarkers(t *testing.T) {
	for _, current := range []string{
		"10 6 * * * /usr/local/bin/x\n# BEGIN GONF Cron[open]\n0 * * * * /bin/e\n",
		"10 6 * * * /usr/local/bin/x\n# END GONF Cron[x]\n",
		"# BEGIN GONF Cron[a]\n10 6 * * * /usr/local/bin/x\n# END GONF Cron[b]\n",
	} {
		if got, changed := adoptUnmanaged(current, identical); changed || got != current {
			t.Fatalf("malformed markers must disable adoption:\n%q\n got %q", current, got)
		}
	}
}

// TestCronAdoptionSelection pins which jobs adopt identical lines by
// default: a present job without WithCronEnv. A job with WithCronEnv runs
// with extra variables (not the same job), and an absent job never adopts.
func TestCronAdoptionSelection(t *testing.T) {
	plain := newCron("j", []opt.CronOption{opt.WithSchedule("10 6 * * *"), opt.WithCommand("/usr/local/bin/x")})
	if a := plain.adoption(); a.identical == nil || *a.identical != *identical.identical || a.legacy != "" ||
		a.name != "j" || a.block != plain.block() {
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
// that already runs it unmanaged: the line becomes the Gonf block in place
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
	want := "MAILTO=root\n" +
		"# BEGIN GONF Cron[gogios-checks]\n*/5 8-22 * * * /usr/local/bin/gogios >/dev/null 2>&1\n# END GONF Cron[gogios-checks]\n" +
		"0 3 * * * /usr/local/bin/keep\n"
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
