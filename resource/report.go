package resource

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/pathtoken"
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
// was simply already converged and is noted StatusOK. Converge calls it for
// every idle resource, so Service, Timer and Package (which is never gated and
// always passes held=false) report an idle or held run identically.
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
	return anyChangedIn(notes, ids)
}

// ChangedSince is AnyChanged restricted to the notes recorded after the
// last change note of anchor in this apply; when anchor has not changed
// yet it is exactly AnyChanged. Daemon-reload uses it with its own ID
// (DaemonReload[system] or DaemonReload[user], which every reload on that
// bus notes under): a change noted before the bus's latest reload is
// already loaded by the manager, so only a later change needs another
// reload. The notes are the apply's ordered outcome log, so "after" is
// apply order.
func ChangedSince(anchor string, ids ...string) bool {
	reportMu.Lock()
	defer reportMu.Unlock()
	from := 0
	for i := len(notes) - 1; i >= 0; i-- {
		if notes[i].id == anchor && isChangeStatus(notes[i].st) {
			from = i + 1
			break
		}
	}
	return anyChangedIn(notes[from:], ids)
}

// anyChangedIn reports whether any of ids changed according to log: a
// change note of the id itself, or for a Directory[path] id also of a File
// under that path (watchCovers). The caller holds reportMu.
func anyChangedIn(log []note, ids []string) bool {
	for _, watch := range ids {
		for _, id := range watchForms(watch) {
			_, isDir := directoryNotePath(id)
			for _, n := range log {
				if !isChangeStatus(n.st) {
					continue
				}
				if n.id == id || (isDir && watchCovers(id, n.id)) {
					return true
				}
			}
		}
	}
	return false
}

// watchForms returns the ids a watch on id matches: id itself and, when it
// carries a destination path token (a DestHome path, File[${HOME}/x]), its
// expanded form too. A plan records resource ids unexpanded, but the apply
// expands the path before it converges the resource, and every kind notes
// its outcome under the id of the path it actually touched
// (File[/home/paul/x]). Without the expanded form an OnChange on a DestHome
// resource never fired. A token that cannot expand here leaves only id.
func watchForms(id string) []string {
	if !pathtoken.HasToken(id) {
		return []string{id}
	}
	expanded, err := pathtoken.Expand(id)
	if err != nil || expanded == id {
		return []string{id}
	}
	return []string{id, expanded}
}

// watchCovers reports whether a change of id fires a gate watching watch:
// id is watch itself, or watch is Directory[p] and id is File[p] or a
// File[…] under p. It is the single copy of that rule, shared by AnyChanged
// (apply time) and RegisteredWatchTargets (merge-time ordering), so a merged
// daemon-reload is ordered after exactly the resources that can fire it.
func watchCovers(watch, id string) bool {
	if id == watch {
		return true
	}
	dirPath, ok := directoryNotePath(watch)
	if !ok {
		return false
	}
	prefix := idPrefix("File") + dirPath
	return id == prefix+"]" || strings.HasPrefix(id, prefix+"/") || strings.HasPrefix(id, prefix+"\\")
}

func isChangeStatus(st Status) bool {
	return st == StatusChanged || st == StatusWouldChange
}

func directoryNotePath(id string) (string, bool) {
	prefix := idPrefix("Directory")
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
	// IDs pass through the logger's redactor (the controller's secret
	// registry, see logger.SetRedactor): recording refuses a strong secret in
	// an identity, but a weak one may remain there.
	for _, n := range interesting {
		_, _ = fmt.Fprint(w, logger.Redact(fmt.Sprintf("  %s %s\n", n.st, n.id)))
	}
}
