package cron

import (
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// fakeCronSecret is synthetic secret material in another (sensitive) job's
// crontab line.
const fakeCronSecret = "fake-cron-token-3d9e"

// crontab's failure output is withheld for every job, for both the read
// (crontab -l) and the write (crontab -): the one table holds every job's
// lines, so a job that is not sensitive itself (Sensitive false here) may
// fail with another job's secret-bearing line in crontab's output.
func TestCrontabFailureOutputIsWithheldForEveryJob(t *testing.T) {
	for _, readFails := range []bool{true, false} {
		for _, sensitive := range []bool{true, false} {
			rs := stubFailingCrontab(t, readFails)
			op := plan.Op{Op: plan.KindCron, ID: "Cron[u/plain]", Name: "plain",
				Command: "/bin/true", Sensitive: sensitive,
				Payload: plan.CronPayload{CronUser: currentCronUser(t), Schedule: "5 * * * *"}}
			err := planHandler{}.Apply(op, plan.ApplyContext{Runners: rs})
			if err == nil || !strings.Contains(err.Error(), "output withheld") || strings.Contains(err.Error(), fakeCronSecret) {
				t.Fatalf("read fails=%v sensitive=%v: err = %v, want the withheld failure without the secret",
					readFails, sensitive, err)
			}
		}
	}
}

// stubFailingCrontab returns a runners.Set faking a crontab holding another
// job's secret-bearing managed block, whose read (readFails) or write fails
// echoing the table.
func stubFailingCrontab(t *testing.T, readFails bool) *runners.Set {
	t.Helper()
	resource.ResetForTest()
	oldDry := resource.DryRun()
	resource.SetDryRun(false)
	table := beginMarker("upload") + "\n5 * * * * /bin/up " + fakeCronSecret + "\n# END GONF Cron[upload]\n"
	cr := &runners.CronRunners{
		Read: func(string, ...string) (string, string, int, error) {
			if readFails {
				return table, "crontab: bad line", 1, nil
			}
			return table, "", 0, nil
		},
		Write: func(stdin string, _ string, _ ...string) (string, string, int, error) {
			return "", "crontab: rejected " + stdin, 1, nil
		},
	}
	t.Cleanup(func() {
		resource.SetDryRun(oldDry)
		resource.ResetForTest()
	})
	return &runners.Set{Cron: cr}
}
