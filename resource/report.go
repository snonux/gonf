package resource

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// Status is the outcome of applying a resource.
type Status int

const (
	// StatusOK means the resource was already in the desired state.
	StatusOK Status = iota
	// StatusChanged means the resource was mutated this apply.
	StatusChanged
	// StatusSkipped means nothing ran (guard passed, target missing, ...).
	StatusSkipped
	// StatusWouldChange is StatusChanged under dry-run: apply would have
	// mutated but made no changes.
	StatusWouldChange
)

// String returns the lowercase report label of s ("ok", "changed",
// "skipped", "would-change").
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

// Like the resource repository, the dry-run flag and report notes are
// process-wide DSL state and deliberately single-goroutine (recipe
// construction happens before fleet fan-out); reportMu only serializes
// individual reads and writes, nothing here is safe for concurrent
// registration/apply sessions.

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

// NoteResult records the apply outcome for a resource id: changed maps to
// StatusChanged, or StatusWouldChange in dry-run mode; otherwise StatusOK.
// It is safe for concurrent use like Note.
func NoteResult(id string, changed bool) {
	st := StatusOK
	if changed {
		st = StatusChanged
		if DryRun() {
			st = StatusWouldChange
		}
	}
	Note(id, st)
}

// NoteIdle notes a resource that had no action to perform this apply. held
// reports that a change gate suppressed its gated action (e.g. a
// restart/reload armed by OnChange with no watched change): that is noted
// StatusSkipped so the summary shows the held action, otherwise the resource
// was simply already converged and is noted StatusOK. Service and Timer share
// it so both report a held gate identically.
func NoteIdle(id string, held bool) {
	if held {
		Note(id, StatusSkipped)
		return
	}
	NoteResult(id, false)
}

// AnyChanged reports whether any of ids was noted as StatusChanged or
// StatusWouldChange. For a Directory[path] id, File notes under that path
// also count (so SyncDir file updates gate daemon-reload).
func AnyChanged(ids ...string) bool {
	reportMu.Lock()
	defer reportMu.Unlock()
	for _, id := range ids {
		if noteChangedLocked(id) {
			return true
		}
		if dirPath, ok := directoryNotePath(id); ok {
			prefix := "File[" + dirPath
			for _, n := range notes {
				if !isChangeStatus(n.st) {
					continue
				}
				if n.id == prefix+"]" || strings.HasPrefix(n.id, prefix+"/") || strings.HasPrefix(n.id, prefix+"\\") {
					return true
				}
			}
		}
	}
	return false
}

func noteChangedLocked(id string) bool {
	for _, n := range notes {
		if n.id == id && isChangeStatus(n.st) {
			return true
		}
	}
	return false
}

func isChangeStatus(st Status) bool {
	return st == StatusChanged || st == StatusWouldChange
}

func directoryNotePath(id string) (string, bool) {
	const prefix = "Directory["
	if !strings.HasPrefix(id, prefix) || !strings.HasSuffix(id, "]") {
		return "", false
	}
	return id[len(prefix) : len(id)-1], true
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

	_, _ = fmt.Fprintf(w, "summary: %d ok, %d changed, %d skipped, %d would-change\n",
		ok, changed, skipped, would)
	for _, n := range interesting {
		_, _ = fmt.Fprintf(w, "  %s %s\n", n.st, n.id)
	}
}
