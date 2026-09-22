package validator

import (
	"testing"
	"time"
)

// Running is true exactly while a validator executes.
func TestRunningTracksValidator(t *testing.T) {
	if Running() {
		t.Fatal("Running before any validator started")
	}
	done := make(chan error, 1)
	go func() { done <- Run("sleep", []string{"1"}) }()

	deadline := time.Now().Add(2 * time.Second)
	for !Running() {
		if time.Now().After(deadline) {
			t.Fatal("Running stayed false while the validator ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatalf("validator: %v", err)
	}
	if Running() {
		t.Fatal("Running still true after the validator returned")
	}
}
