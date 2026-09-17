package resource_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/snonux/gonf/resource"
)

// noteLine returns the summary line resource.PrintSummary recorded for id,
// or "" when id has no non-OK note (PrintSummary only lists changed/
// would-change entries).
func noteLine(t *testing.T, id string) string {
	t.Helper()
	var buf bytes.Buffer
	resource.PrintSummary(&buf)
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, id) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func TestMutateDryRunSkipsFn(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	resource.SetDryRun(true)

	var called bool
	err := resource.Mutate("Fit[dry]", "do the fit thing", func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate returned %v, want nil", err)
	}
	if called {
		t.Fatal("Mutate must not call fn under dry-run")
	}
	if got := noteLine(t, "Fit[dry]"); !strings.HasPrefix(got, "would-change") {
		t.Fatalf("note = %q, want a would-change line", got)
	}
}

func TestMutateRunsFnAndNotesChanged(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)

	var called bool
	err := resource.Mutate("Fit[real]", "do the fit thing", func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate returned %v, want nil", err)
	}
	if !called {
		t.Fatal("Mutate must call fn when dry-run is off")
	}
	if got := noteLine(t, "Fit[real]"); !strings.HasPrefix(got, "changed") {
		t.Fatalf("note = %q, want a changed line", got)
	}
}

func TestMutatePropagatesFnError(t *testing.T) {
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)

	wantErr := errors.New("boom")
	err := resource.Mutate("Fit[err]", "do the fit thing", func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Mutate error = %v, want %v", err, wantErr)
	}
	// A failed mutation is not a completed one: Mutate itself records no
	// note, leaving that decision to the caller (mirrors the ad-hoc checks
	// it replaces, which never noted StatusChanged on a failed mutation
	// either).
	if got := noteLine(t, "Fit[err]"); got != "" {
		t.Fatalf("note = %q, want none recorded by Mutate on error", got)
	}
}

func TestMutateDryRunNeverCallsFnEvenOnWouldBeError(t *testing.T) {
	// Regression guard for the exact bug class t5 targets: under dry-run,
	// fn's mutating body must never run, even if it would return an error a
	// real apply would need to surface.
	resource.ResetForTest()
	t.Cleanup(resource.ResetForTest)
	resource.SetDryRun(true)

	err := resource.Mutate("Fit[dry-err]", "do the fit thing", func() error {
		t.Fatal("fn must not run under dry-run")
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate returned %v, want nil", err)
	}
}
