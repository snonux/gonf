package clihost

import "testing"

// TestMarker pins the marker lifecycle: MarkActive sets it and its restore
// brings back the previous (unset) state, nested marks restore in order, and
// SetForTest restores the previous value too.
func TestMarker(t *testing.T) {
	defer SetForTest(false)()
	if Active() {
		t.Fatal("Active() = true after SetForTest(false)")
	}
	restore := MarkActive()
	if !Active() {
		t.Fatal("Active() = false after MarkActive")
	}
	inner := MarkActive()
	inner()
	if !Active() {
		t.Fatal("a nested MarkActive restore cleared the outer mark")
	}
	restore()
	if Active() {
		t.Fatal("Active() = true after the MarkActive restore")
	}
	undo := SetForTest(true)
	undo()
	if Active() {
		t.Fatal("SetForTest restore did not bring back the unset state")
	}
}
