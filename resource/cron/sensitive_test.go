package cron

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// fakeCronSecret is synthetic secret material in a cron command.
const fakeCronSecret = "fake-cron-token-3d9e"

// A sensitive cron op withholds crontab's failure output, for both the
// read (crontab -l) and the write (crontab -), while a plain op keeps it:
// crontab may quote the offending line or the whole table.
func TestSensitiveCronWithholdsCrontabFailureOutput(t *testing.T) {
	tests := []struct {
		name      string
		readFails bool
	}{
		{name: "read", readFails: true},
		{name: "write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubFailingCrontab(t, tt.readFails)
			op := plan.Op{Op: plan.KindCron, ID: "Cron[u/upload]", Name: "upload", CronUser: currentCronUser(t),
				Command: "/bin/up " + fakeCronSecret, Schedule: "5 * * * *", Sensitive: true}

			err := planHandler{}.Apply(op, plan.ApplyContext{})
			if err == nil || !strings.Contains(err.Error(), "output withheld") || strings.Contains(err.Error(), fakeCronSecret) {
				t.Fatalf("sensitive cron: err = %v, want the withheld failure without the secret", err)
			}
			op.Sensitive = false
			resource.ResetForTest()
			err = planHandler{}.Apply(op, plan.ApplyContext{})
			if err == nil || !strings.Contains(err.Error(), fakeCronSecret) {
				t.Fatalf("plain cron: err = %v, want crontab's output", err)
			}
		})
	}
}

// stubFailingCrontab fakes a crontab whose read (readFails) or write fails,
// echoing the secret-bearing table in its output.
func stubFailingCrontab(t *testing.T, readFails bool) {
	t.Helper()
	resource.ResetForTest()
	oldDry := resource.DryRun()
	resource.SetDryRun(false)
	table := "5 * * * * /bin/up " + fakeCronSecret + "\n"
	SetRunnersForTest(
		func(string, ...string) (string, string, int, error) {
			if readFails {
				return table, "crontab: bad line", 1, nil
			}
			return "", "", 0, nil
		},
		func(stdin string, _ string, _ ...string) (string, string, int, error) {
			return "", "crontab: rejected " + stdin, 1, nil
		},
	)
	t.Cleanup(func() {
		ResetRunnersForTest()
		resource.SetDryRun(oldDry)
		resource.ResetForTest()
	})
}
