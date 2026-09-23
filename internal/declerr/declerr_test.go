package declerr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// These tests share the package-global state and must not run in parallel.

func reset(t *testing.T) {
	t.Helper()
	Reset()
	t.Cleanup(Reset)
}

// TestFirstReportWins: only the first report is kept, its message is
// unchanged, it wraps the original error, and it carries this file's line.
func TestFirstReportWins(t *testing.T) {
	reset(t)
	cause := errors.New("Task: name must not be empty")
	Report(cause)
	Reportf("Host %q already registered", "h")
	Report(nil) // ignored
	err := First()
	if err == nil || err.Error() != cause.Error() {
		t.Fatalf("First = %v, want exactly %q", err, cause)
	}
	if !errors.Is(err, cause) {
		t.Fatal("the report does not wrap the original error")
	}
	if loc := Location(err); !strings.Contains(loc, "declerr_test.go:") {
		t.Fatalf("Location = %q, want this test file", loc)
	}
}

// TestNoReportNoError is the negative control.
func TestNoReportNoError(t *testing.T) {
	reset(t)
	if err := First(); err != nil {
		t.Fatalf("First = %v without a report", err)
	}
	if loc := Location(errors.New("plain")); loc != "" {
		t.Fatalf("Location of a plain error = %q, want empty", loc)
	}
}

// TestCaptureRoutesReportsAndRestores: while a sink is installed every report
// goes to it (none becomes sticky), and restore reinstates the previous sink.
func TestCaptureRoutesReportsAndRestores(t *testing.T) {
	reset(t)
	var outer, inner []error
	restoreOuter := Capture(func(err error) { outer = append(outer, err) })
	restoreInner := Capture(func(err error) { inner = append(inner, err) })
	Reportf("one")
	restoreInner()
	Reportf("two")
	restoreOuter()
	if len(inner) != 1 || inner[0].Error() != "one" || len(outer) != 1 || outer[0].Error() != "two" {
		t.Fatalf("inner %v, outer %v; want [one] and [two]", inner, outer)
	}
	if err := First(); err != nil {
		t.Fatalf("a captured report became sticky: %v", err)
	}
	Reportf("three")
	if err := First(); err == nil || err.Error() != "three" {
		t.Fatalf("First after restore = %v, want three", err)
	}
}

// TestReportKeepsExistingLocation: re-reporting an error that already is (or
// wraps) a declaration Error keeps its original location instead of the
// re-reporting frame.
func TestReportKeepsExistingLocation(t *testing.T) {
	reset(t)
	var got error
	restore := Capture(func(err error) { got = err })
	Reportf("first")
	restore()
	Report(fmt.Errorf("wrapped: %w", got))
	if First() == nil || Location(First()) != Location(got) {
		t.Fatalf("location %q, want the original %q", Location(First()), Location(got))
	}
}
