package api

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// recordEncoded records body as the one task of a fresh plan and returns
// the encoded plan.jsonl bytes, so two spellings can be compared byte for
// byte.
func recordEncoded(t *testing.T, body func()) string {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("sugar", "sugar spelling", body)
	ops, err := RecordPlan("sugar", "", "sugar")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	data, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	return string(data)
}

// requireSamePlan records both spellings and fails unless their plans are
// byte-identical.
func requireSamePlan(t *testing.T, sugar, long func()) string {
	t.Helper()
	got, want := recordEncoded(t, sugar), recordEncoded(t, long)
	if got != want {
		t.Fatalf("plans differ\nsugar:\n%s\nlong form:\n%s", got, want)
	}
	return got
}

// TestCronCompactFormsRecordTheOptionStylePlan pins that CronAt and
// WithSchedule are pure spelling: each records byte-for-byte the plan of the
// per-field option style, extra options included, and so does a schedule
// separated by several blanks.
func TestCronCompactFormsRecordTheOptionStylePlan(t *testing.T) {
	long := func() {
		Cron("backup", options.WithCommand("/usr/local/sbin/x base"),
			options.WithMinute("10"), options.WithHour("6"),
			options.WithCronUser("paul"), options.WithCronEnv("PATH=/bin"))
	}
	encoded := requireSamePlan(t, func() {
		CronAt("backup", "10 6 * * *", "/usr/local/sbin/x base",
			options.WithCronUser("paul"), options.WithCronEnv("PATH=/bin"))
	}, long)
	if !strings.Contains(encoded, `"schedule":"10 6 * * *"`) {
		t.Fatalf("plan lacks the schedule:\n%s", encoded)
	}
	requireSamePlan(t, func() {
		Cron("backup", options.WithSchedule("10\t6  *  * *"), options.WithCommand("/usr/local/sbin/x base"),
			options.WithCronUser("paul"), options.WithCronEnv("PATH=/bin"))
	}, long)
}

// TestCronLegacyCommandPlanUnchanged pins that a recipe still passing
// WithLegacyCommand with its own command records exactly what it did before
// identical-line adoption became the default: legacy_command stays on the
// wire (and keeps its command-only, any-schedule match on the destination).
func TestCronLegacyCommandPlanUnchanged(t *testing.T) {
	encoded := recordEncoded(t, func() {
		Cron("gogios", options.WithCommand("/usr/local/bin/gogios"),
			options.WithLegacyCommand("/usr/local/bin/gogios"), options.WithMinute("*/5"))
	})
	want := `{"op":"cron","id":"Cron[root/gogios]","name":"gogios","cron_user":"root",` +
		`"command":"/usr/local/bin/gogios","legacy_command":"/usr/local/bin/gogios","schedule":"*/5 * * * *"}`
	if !strings.Contains(encoded, want+"\n") {
		t.Fatalf("plan changed:\n%s\nwant line %s", encoded, want)
	}
}

// TestCronScheduleMisuse pins that a bad compact schedule is a declaration
// error with the recipe line, whichever spelling carries it.
func TestCronScheduleMisuse(t *testing.T) {
	for _, tc := range []struct {
		name, schedule, want string
	}{
		{"four fields", "10 6 * *", "must have 5 fields"},
		{"six fields", "0 10 6 * * *", "must have 5 fields"},
		{"reboot", "@reboot", "@ directives such as @reboot are not supported"},
		{"range", "61 * * * *", `minute field "61"`},
		{"weekday", "* * * * 8", `weekday field "8"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireDeclErr(t, tc.want, func() { CronAt("job", tc.schedule, "/bin/true") })
			requireDeclErr(t, tc.want, func() {
				Cron("job", options.WithSchedule(tc.schedule), options.WithCommand("/bin/true"))
			})
		})
	}
}

// TestShRecordsTheCommandPlan pins that Sh is Command with a shell-words
// argv: the plan is byte-identical, with and without WithName, and the
// default ID is Command's (the words joined by single spaces).
func TestShRecordsTheCommandPlan(t *testing.T) {
	requireSamePlan(t, func() {
		Sh(`systemctl  restart 'my unit' "a\"b" c\ d`)
	}, func() {
		Command("systemctl", List("restart", "my unit", `a"b`, "c d"))
	})
	encoded := requireSamePlan(t, func() {
		Sh("newaliases", options.WithName("aliases"), options.Creates("/etc/mail/aliases.db"))
	}, func() {
		Command("newaliases", nil, options.WithName("aliases"), options.Creates("/etc/mail/aliases.db"))
	})
	if !strings.Contains(encoded, `"id":"Command[aliases]"`) {
		t.Fatalf("named Sh lost its ID:\n%s", encoded)
	}
	if got := recordEncoded(t, func() { Sh("echo 'a b'") }); !strings.Contains(got, `"id":"Command[echo a b]"`) {
		t.Fatalf("Sh default ID is not Command's:\n%s", got)
	}
}

// TestShMisuse pins that a command Sh cannot split without a shell is a
// declaration error.
func TestShMisuse(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"", "must not be empty"},
		{"echo 'open", "unterminated single quote"},
		{"ls | wc -l", `unquoted '|'`},
		{"echo $HOME", `unquoted '$'`},
		{"rm /tmp/*.log", `unquoted '*'`},
	} {
		requireDeclErr(t, tc.want, func() { Sh(tc.command) })
	}
}

// TestNoopRecordsAndReportsOk pins Noop end to end: it records a noop op
// under Noop[name] that raises the plan header to schema 25, and applying
// it notes ok and changes nothing.
func TestNoopRecordsAndReportsOk(t *testing.T) {
	encoded := recordEncoded(t, func() { Noop("ping") })
	if !strings.Contains(encoded, `{"op":"noop","id":"Noop[ping]","name":"ping"}`) ||
		!strings.Contains(encoded, `"version":25`) {
		t.Fatalf("noop plan:\n%s", encoded)
	}

	ResetForTest()
	t.Cleanup(ResetForTest)
	Noop("ping")
	if err := Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var summary strings.Builder
	resource.PrintSummary(&summary)
	if !strings.HasPrefix(summary.String(), "summary: 1 ok, 0 changed, 0 skipped, 0 would-change\n") {
		t.Fatalf("Noop[ping] must report ok and change nothing, summary:\n%s", summary.String())
	}
	requireDeclErr(t, "name must not be empty", func() { Noop(" ") })
}

// TestPackagesVariadic pins that Packages(names...) records what
// Package(List(names...)) does.
func TestPackagesVariadic(t *testing.T) {
	requireSamePlan(t, func() { Packages("git", "tmux") }, func() { Package(List("git", "tmux")) })
}

// TestServiceFlagsRecord pins the WithFlags plan op: flags and has_flags on
// the service line, empty flags kept as managed, and the header raised to
// schema 25 only when a service manages flags.
func TestServiceFlagsRecord(t *testing.T) {
	encoded := recordEncoded(t, func() { Service("httpd", options.WithFlags(""), options.WithRestart) })
	if !strings.Contains(encoded, `{"op":"service","id":"Service[httpd]","name":"httpd","restart":true,"has_flags":true}`) ||
		!strings.Contains(encoded, `"version":25`) {
		t.Fatalf("empty-flags plan:\n%s", encoded)
	}
	encoded = recordEncoded(t, func() { Service("nsd", options.WithFlags("-c /var/nsd/etc/nsd.conf")) })
	if !strings.Contains(encoded, `"flags":"-c /var/nsd/etc/nsd.conf","has_flags":true`) {
		t.Fatalf("flags plan:\n%s", encoded)
	}
	if encoded = recordEncoded(t, func() { Service("httpd") }); strings.Contains(encoded, "flags") ||
		!strings.Contains(encoded, `"version":21`) {
		t.Fatalf("a service without WithFlags changed its plan:\n%s", encoded)
	}
	requireDeclErr(t, "WithFlags cannot be used with an absent service", func() {
		NoService("httpd", options.WithFlags("-v"))
	})
	requireDeclErr(t, "WithFlags must be a single line", func() {
		Service("httpd", options.WithFlags("-v\n-x"))
	})
}
