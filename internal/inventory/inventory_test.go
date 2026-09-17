package inventory

import (
	"fmt"
	"sync"
	"testing"
)

// TestHostValueConcurrentWithSetHostValue is a regression test for a data
// race that existed when callers looked up a Host via LookupHost, released
// mu, then indexed the returned Host's Values map directly: a shallow copy
// of Host still aliases the same underlying map stored in the registry (map
// fields are reference types), so that post-unlock index was an
// unsynchronized read racing against SetHostValue's in-place mutation of
// the same map under mu.
//
// HostValue closes that hole by doing the lookup-and-index atomically under
// one lock, so it never exposes the map to a caller outside a locked
// section. Run with -race; this must be race-free.
func TestHostValueConcurrentWithSetHostValue(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	AddHost("racer")
	SetHostValue("racer", "seed", 0)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		i := i
		// Writer: grows the SAME host's Values map with a distinct key per
		// goroutine (SetHostValue fails fast on a duplicate key, which is
		// not what this test is exercising).
		go func() {
			defer wg.Done()
			SetHostValue("racer", fmt.Sprintf("key-%d", i), i)
		}()
		// Reader: repeatedly reads a key set before the concurrent phase,
		// concurrently with the writers above mutating the same map.
		go func() {
			defer wg.Done()
			v, hostFound, keyFound := HostValue("racer", "seed")
			if !hostFound || !keyFound {
				t.Errorf("HostValue(racer, seed) hostFound=%v keyFound=%v, want true,true", hostFound, keyFound)
				return
			}
			if v.(int) != 0 {
				t.Errorf("HostValue(racer, seed) = %v, want 0", v)
			}
		}()
	}
	wg.Wait()
}
