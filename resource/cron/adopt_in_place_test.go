package cron

import (
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
)

// TestEnsureAdoptsIdenticalLineBeforeAnotherJobsEnv reproduces the crontab
// that made a job run twice: the unmanaged line sits before another Gonf
// block whose WithCronEnv sets PATH. The line must still be adopted, and the
// managed block takes its place (not the end of the table), so the job keeps
// the environment it ran with. A second apply is a no-op.
func TestEnsureAdoptsIdenticalLineBeforeAnotherJobsEnv(t *testing.T) {
	tab := "* * * * * /usr/local/bin/keep.sh >/dev/null 2>&1\n" +
		"* * * * * /usr/local/bin/failback.sh\n" +
		"# BEGIN GONF Cron[other]\nPATH=/usr/bin:/bin:/usr/local/bin\n5 * * * * /usr/local/sbin/other daily\n# END GONF Cron[other]\n"
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
		opt.WithSchedule("* * * * *"), opt.WithCommand("/usr/local/bin/failback.sh")}
	for range 2 {
		if err := EnsureWith(cr, "failback", opts...); err != nil {
			t.Fatalf("EnsureWith: %v", err)
		}
	}
	want := "* * * * * /usr/local/bin/keep.sh >/dev/null 2>&1\n" +
		"# BEGIN GONF Cron[failback]\n* * * * * /usr/local/bin/failback.sh\n# END GONF Cron[failback]\n" +
		"# BEGIN GONF Cron[other]\nPATH=/usr/bin:/bin:/usr/local/bin\n5 * * * * /usr/local/sbin/other daily\n# END GONF Cron[other]\n"
	if tab != want || writes != 1 {
		t.Fatalf("writes=%d tab:\n%s\nwant:\n%s", writes, tab, want)
	}
}
