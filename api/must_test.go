package api

import (
	"fmt"
	"sync"
	"testing"
)

// TestMustHostValueRaceWithSetValue is a regression test for a data race
// introduced when the host registry moved to internal/inventory: LookupHost
// locked its mutex, copied the Host struct, unlocked, and returned the copy
// -- but Host.Values is a map, so the copy still aliased the exact map
// stored in the registry. MustHostValue then indexed rec.Values[key] AFTER
// the lock was released, racing against SetValue/SetHostValue mutating that
// same aliased map under lock on another goroutine. The fix (HostValue in
// internal/inventory) performs the lookup-and-index atomically under one
// lock, so MustHostValue never touches the map outside a locked section.
//
// This must pass under `go test -race`. Before the fix, running the
// equivalent access pattern (lookup, unlock, then index) reliably tripped
// the race detector.
func TestMustHostValueRaceWithSetValue(t *testing.T) {
	ResetInventory()
	t.Cleanup(ResetInventory)

	h := Host("racer", WithValue("seed", 0))

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		i := i
		// Writer: mutates the SAME host's Values map concurrently with the
		// readers below (distinct keys per goroutine so SetValue's
		// duplicate-key fail-fast never fires).
		go func() {
			defer wg.Done()
			h.SetValue(fmt.Sprintf("key-%d", i), i)
		}()
		// Reader: reads a key that was set before the concurrent phase
		// started, so MustHostValue never hits its own fail-fast paths --
		// only the race on the underlying map is under test here.
		go func() {
			defer wg.Done()
			if got := MustHostValue[int]("racer", "seed"); got != 0 {
				t.Errorf("MustHostValue(racer, seed) = %d, want 0", got)
			}
		}()
	}
	wg.Wait()
}
