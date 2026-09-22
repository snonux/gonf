package clihost

import (
	"sync"
	"testing"
)

// TestMarker pins the marker lifecycle: MarkActive sets it and its release
// clears it again, a second release of the same mark does nothing, and
// SetForTest restores the previous state.
func TestMarker(t *testing.T) {
	defer SetForTest(false)()
	if Active() {
		t.Fatal("Active() = true after SetForTest(false)")
	}
	release := MarkActive()
	if !Active() {
		t.Fatal("Active() = false after MarkActive")
	}
	release()
	release() // idempotent: must not end another call's mark
	if Active() {
		t.Fatal("Active() = true after the MarkActive release")
	}
	undo := SetForTest(true)
	undo()
	if Active() {
		t.Fatal("SetForTest restore did not bring back the unset state")
	}
}

// TestMarkerOverlappingCalls pins the counter: overlapping CLI calls that end
// in any order keep the marker set until the last one ends. With the old
// swap-and-restore flag, releasing the first mark before the second restored
// "unset" while the second call still ran. A released mark released again
// must not end the other one either.
func TestMarkerOverlappingCalls(t *testing.T) {
	defer SetForTest(false)()
	first := MarkActive()
	second := MarkActive()
	first()
	first()
	if !Active() {
		t.Fatal("Active() = false while the second CLI call still runs")
	}
	second()
	if Active() {
		t.Fatal("Active() = true after every CLI call ended")
	}
}

// TestMarkerConcurrentCalls runs many overlapping marks from goroutines
// (under -race in the gates) and requires the marker set throughout and
// clear once all have ended.
func TestMarkerConcurrentCalls(t *testing.T) {
	defer SetForTest(false)()
	outer := MarkActive()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := MarkActive()
			if !Active() {
				t.Error("Active() = false inside a running mark")
			}
			release()
		}()
	}
	wg.Wait()
	if !Active() {
		t.Fatal("goroutine releases ended the outer mark")
	}
	outer()
	if Active() {
		t.Fatal("Active() = true after every mark ended")
	}
}
