package cron

import (
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
)

// memCrontab returns crontab runners backed by *tab, so each call site gets
// its own independent in-memory crontab.
func memCrontab(tab *string) *runners.CronRunners {
	return &runners.CronRunners{
		Read: func(string, ...string) (string, string, int, error) { return *tab, "", 0, nil },
		Write: func(stdin, _ string, _ ...string) (string, string, int, error) {
			*tab = stdin
			return "", "", 0, nil
		},
	}
}

// TestInjectedCronRunnersArePerApply pins task fg2's per-apply injection:
// two plan applies built up front with different CronRunners (applied in
// reverse of construction order) each write only their own crontab, so no
// process-global state is shared between them. It needs no t.Setenv
// no-parallel guard, unlike the internal/testseam fakes it replaces.
func TestInjectedCronRunnersArePerApply(t *testing.T) {
	var tabA, tabB string
	ctxA := plan.ApplyContext{Runners: &runners.Set{Cron: memCrontab(&tabA)}}
	ctxB := plan.ApplyContext{Runners: &runners.Set{Cron: memCrontab(&tabB)}}
	user := currentCronUser(t)
	op := func(name string) plan.Op {
		return plan.Op{Op: plan.KindCron, ID: "Cron[" + user + "/" + name + "]", Name: name,
			Command: "/bin/true", Payload: plan.CronPayload{CronUser: user, Schedule: "5 * * * *"}}
	}
	if err := (planHandler{}).Apply(op("b-job"), ctxB); err != nil {
		t.Fatalf("apply B: %v", err)
	}
	if err := (planHandler{}).Apply(op("a-job"), ctxA); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	if !strings.Contains(tabA, beginMarker("a-job")) || strings.Contains(tabA, "b-job") {
		t.Fatalf("crontab A = %q, want only a-job", tabA)
	}
	if !strings.Contains(tabB, beginMarker("b-job")) || strings.Contains(tabB, "a-job") {
		t.Fatalf("crontab B = %q, want only b-job", tabB)
	}
}

// TestNewCronWithLockChoice pins the lock strategy the injected runners
// select: the real cross-process lock without injection or with
// CrossProcessLock, the in-process one for a faked crontab otherwise.
func TestNewCronWithLockChoice(t *testing.T) {
	tests := []struct {
		name string
		cr   *runners.CronRunners
		want bool
	}{
		{"real runners", nil, false},
		{"faked crontab", &runners.CronRunners{}, true},
		{"faked crontab keeping the real lock", &runners.CronRunners{CrossProcessLock: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCronWith(tt.cr, "job", []opt.CronOption{opt.WithCommand("/bin/true")})
			if c.inProcessLock != tt.want {
				t.Fatalf("inProcessLock = %t, want %t", c.inProcessLock, tt.want)
			}
		})
	}
}
