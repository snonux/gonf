package api

import (
	"reflect"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
	svc "github.com/snonux/gonf/resource/service"
)

// fakeRcctlFlags fakes OpenBSD rcctl for a running, enabled daemon whose
// stored flags are current, recording every mutating call.
func fakeRcctlFlags(calls *[][]string, current string) *runners.ServiceRunners {
	run := func(name string, args ...string) (string, string, int, error) {
		if name != "rcctl" {
			return "", "unexpected bin " + name, 1, nil
		}
		switch {
		case args[0] == "check" || (args[0] == "get" && args[2] == "status"):
			return "", "", 0, nil
		case args[0] == "get" && args[2] == "flags":
			return current + "\n", "", 0, nil
		}
		*calls = append(*calls, append([]string(nil), args...))
		return "", "", 0, nil
	}
	return &runners.ServiceRunners{Run: run, Manager: func() (string, error) { return "rcctl", nil }}
}

// TestPlanOptionFitness_ServiceFlags pins that WithFlags survives the plan
// round trip (schema v25 flags/has_flags): a direct Ensure and a recorded
// plan apply make the same rcctl calls, empty flags included, and a flags
// change restarts a WithRestart service even behind an OnChange gate whose
// watched resource (a Noop, never changed) did not fire.
func TestPlanOptionFitness_ServiceFlags(t *testing.T) {
	for _, c := range []struct {
		name, flags, current string
		want                 [][]string
	}{
		{"Set", "-d -v", "", [][]string{{"set", "optfitsvc", "flags", "-d -v"}, {"restart", "optfitsvc"}}},
		{"SetEmpty", "", "-v", [][]string{{"set", "optfitsvc", "flags"}, {"restart", "optfitsvc"}}},
		{"Converged", "-v", "-v", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := []opt.ServiceOption{opt.WithFlags(c.flags), opt.WithRestart, opt.WatchChanges("Noop[quiet]")}
			var directCalls [][]string
			if err := svc.EnsureWith(fakeRcctlFlags(&directCalls, c.current), nil, "optfitsvc", opts...); err != nil {
				t.Fatalf("direct Ensure: %v", err)
			}
			var planCalls [][]string
			recordApplyOptionWithRunners(t, "service_flags_"+c.name,
				&runners.Set{Service: fakeRcctlFlags(&planCalls, c.current)}, func() {
					quiet := Noop("quiet")
					Service("optfitsvc", opt.WithFlags(c.flags), opt.WithRestart, opt.OnChange(quiet))
				})
			if !reflect.DeepEqual(directCalls, c.want) || !reflect.DeepEqual(planCalls, c.want) {
				t.Fatalf("calls:\n direct: %v\n plan:   %v\n want:   %v", directCalls, planCalls, c.want)
			}
		})
	}
}
