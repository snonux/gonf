package pkg

import (
	"slices"
	"testing"

	"github.com/snonux/gonf/internal/runners"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// recordingDNF returns package runners forcing the dnf backend, reporting
// every package as not installed and appending each dnf invocation's
// arguments to *calls.
func recordingDNF(calls *[][]string) *runners.PackageRunners {
	return &runners.PackageRunners{
		Manager: func() (string, error) { return "dnf", nil },
		Run: func(name string, args ...string) (string, string, int, error) {
			if name == "rpm" {
				return "", "not installed", 1, nil
			}
			*calls = append(*calls, slices.Clone(args))
			return "", "", 0, nil
		},
	}
}

// TestInjectedPackageRunnersArePerApply pins task fg2's per-apply injection:
// two plan applies built up front with different PackageRunners (applied in
// reverse of construction order) each reach only their own runner and
// detector, so no process-global state is shared between them. It needs no
// t.Setenv no-parallel guard, unlike the internal/testseam fakes it
// replaces.
func TestInjectedPackageRunnersArePerApply(t *testing.T) {
	oldDry := resource.DryRun()
	t.Cleanup(func() { resource.SetDryRun(oldDry) })
	resource.SetDryRun(false)

	var callsA, callsB [][]string
	ctxA := plan.ApplyContext{Runners: &runners.Set{Package: recordingDNF(&callsA)}}
	ctxB := plan.ApplyContext{Runners: &runners.Set{Package: recordingDNF(&callsB)}}
	op := func(name string) plan.Op {
		return plan.Op{Op: plan.KindPackage, ID: "Package[" + name + "]", Name: name}
	}
	if err := (planHandler{}).Apply(op("pkg-b"), ctxB); err != nil {
		t.Fatalf("apply B: %v", err)
	}
	if err := (planHandler{}).Apply(op("pkg-a"), ctxA); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	wantA := [][]string{{"install", "-y", "pkg-a"}}
	wantB := [][]string{{"install", "-y", "pkg-b"}}
	if !slices.EqualFunc(callsA, wantA, slices.Equal) || !slices.EqualFunc(callsB, wantB, slices.Equal) {
		t.Fatalf("dnf calls A = %v, B = %v; want %v and %v", callsA, callsB, wantA, wantB)
	}
}
