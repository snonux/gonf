package resource

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
)

// resetRaceReports is how many reports TestResetDeclarationErrorNeverLosesConcurrentReport
// races against ResetDeclarationError. Before task kg2 roughly half of all
// reports were lost (with or without -race), so a few thousand fail it every
// time while keeping the passing case well under a second.
const resetRaceReports = 5000

// TestResetDeclarationErrorNeverLosesConcurrentReport pins task kg2's
// atomicity fix: ResetDeclarationError must hand back exactly what it
// cleared. It used to read declerr.First() and then clear the slot with a
// second, separate lock acquisition (declerr.ResetFirst), so a report that
// landed between the two was wiped without ever being returned — silently
// defeating task vf2's "the caller must look at what it discards" contract.
//
// One goroutine reports resetRaceReports distinct errors, each only once the
// sticky slot is empty again (so declerr's first-wins rule never drops one
// legitimately); the test goroutine keeps calling ResetDeclarationError and
// counts what it gets back. Every report must come back exactly once. A lost
// report leaves the slot empty without being returned, so the reporter moves
// on and the count falls short. Only declerr's own mutex-guarded slot is
// touched concurrently — the repository stays single-goroutine — and the
// test does not run in parallel because declerr's state is process-global.
func TestResetDeclarationErrorNeverLosesConcurrentReport(t *testing.T) {
	declerr.Reset()
	t.Cleanup(declerr.Reset)
	// Pre-wrapped *declerr.Error values skip Report's stack walk, so a report
	// reaches declerr's lock as quickly as a reset does and the two
	// interleave as tightly as possible.
	reports := make([]error, resetRaceReports)
	for i := range reports {
		reports[i] = &declerr.Error{Err: fmt.Errorf("concurrent declaration error %d", i)}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(done)
		for _, err := range reports {
			// Wait for the reset loop to take the previous report; yielding
			// keeps this spin from starving the reset loop of declerr's lock.
			for declerr.First() != nil {
				runtime.Gosched()
			}
			declerr.Report(err)
		}
	})
	returned := drainResets(done)
	wg.Wait()
	if len(returned) != resetRaceReports {
		t.Fatalf("ResetDeclarationError returned %d of %d reports: a concurrent report "+
			"was cleared without being returned (task kg2)", len(returned), resetRaceReports)
	}
	for i, err := range returned {
		if !errors.Is(err, reports[i]) {
			t.Fatalf("report %d came back as %v, want %v", i, err, reports[i])
		}
	}
}

// drainResets calls ResetDeclarationError until done is closed and one final
// call finds nothing left, returning every non-nil error it handed back in
// order.
func drainResets(done <-chan struct{}) []error {
	var returned []error
	for {
		select {
		case <-done:
			if err := ResetDeclarationError(); err != nil {
				returned = append(returned, err)
			}
			return returned
		default:
			if err := ResetDeclarationError(); err != nil {
				returned = append(returned, err)
			}
		}
	}
}
