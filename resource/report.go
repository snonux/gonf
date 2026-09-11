package resource

import (
	"fmt"
	"io"
	"sync"
)

// Status is the outcome of applying a resource.
type Status int

const (
	StatusOK Status = iota
	StatusChanged
	StatusSkipped
	StatusWouldChange
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusChanged:
		return "changed"
	case StatusSkipped:
		return "skipped"
	case StatusWouldChange:
		return "would-change"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

type note struct {
	id string
	st Status
}

var (
	reportMu sync.Mutex
	dryRun   bool
	notes    []note
)

// SetDryRun enables or disables dry-run mode (no mutating syscalls).
func SetDryRun(v bool) {
	reportMu.Lock()
	defer reportMu.Unlock()
	dryRun = v
}

// DryRun reports whether dry-run mode is active.
func DryRun() bool {
	reportMu.Lock()
	defer reportMu.Unlock()
	return dryRun
}

// ResetReport clears recorded outcomes. Called at the start of each Apply.
func ResetReport() {
	reportMu.Lock()
	defer reportMu.Unlock()
	notes = nil
}

// Note records the outcome for a resource id.
func Note(id string, st Status) {
	reportMu.Lock()
	defer reportMu.Unlock()
	notes = append(notes, note{id: id, st: st})
}

// PrintSummary writes counts and non-OK resource ids to w.
func PrintSummary(w io.Writer) {
	reportMu.Lock()
	defer reportMu.Unlock()

	var ok, changed, skipped, would int
	var interesting []note
	for _, n := range notes {
		switch n.st {
		case StatusOK:
			ok++
		case StatusChanged:
			changed++
			interesting = append(interesting, n)
		case StatusSkipped:
			skipped++
		case StatusWouldChange:
			would++
			interesting = append(interesting, n)
		}
	}

	fmt.Fprintf(w, "summary: %d ok, %d changed, %d skipped, %d would-change\n",
		ok, changed, skipped, would)
	for _, n := range interesting {
		fmt.Fprintf(w, "  %s %s\n", n.st, n.id)
	}
}
