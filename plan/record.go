package plan

import (
	"os"
	"sync"

	opt "github.com/snonux/gonf/api/options"
)

var (
	recordMu  sync.Mutex
	recording bool
	recorded  []Op
)

// Recording, the draft recorder (resource.SetPlanDraftRecorder), and the api
// recording session form the record-mode trio: RecordPlanTo (api/plan.go)
// always sets and clears all three together for one recording session. They
// live in separate packages because importing api from here would be a
// cycle; the trio relationship is documented here and in resource/draft.go.

// SetRecording enables or disables plan-record mode. When enabled, resource
// Present paths append Op lines via Record instead of relying on Apply.
func SetRecording(v bool) {
	recordMu.Lock()
	defer recordMu.Unlock()
	recording = v
}

// Recording reports whether plan-record mode is active.
func Recording() bool {
	recordMu.Lock()
	defer recordMu.Unlock()
	return recording
}

// ResetRecord clears recorded ops. Call before a new RecordPlan session.
func ResetRecord() {
	recordMu.Lock()
	defer recordMu.Unlock()
	recorded = nil
}

// ResetForTest is the single canonical test seam for plan record state: it
// disables recording and clears recorded ops. SetRecording/ResetRecord stay
// available individually so existing tests keep working.
func ResetForTest() {
	SetRecording(false)
	ResetRecord()
}

// Record appends op when recording is enabled; otherwise it is a no-op.
func Record(op Op) {
	recordMu.Lock()
	defer recordMu.Unlock()
	if !recording {
		return
	}
	recorded = append(recorded, op)
}

// Recorded returns a copy of ops recorded since the last ResetRecord (no header).
func Recorded() []Op {
	recordMu.Lock()
	defer recordMu.Unlock()
	out := make([]Op, len(recorded))
	copy(out, recorded)
	return out
}

// FinishRecord prepends a versioned plan header and returns the full op list.
func FinishRecord(id string) []Op {
	body := Recorded()
	out := make([]Op, 0, 1+len(body))
	out = append(out, Op{Op: KindPlan, Version: CurrentVersion, ID: id})
	out = append(out, body...)
	return out
}

// FormatMode formats a permission mask in the canonical plan-wire octal form
// (e.g. "0640"; four digits when setuid/setgid/sticky are set, e.g. "04755").
// It delegates to opt.ModeToWire, the single mode-serialization helper shared
// with the resource PlanDraft sites, so both directions of the mode
// representation agree.
func FormatMode(mode os.FileMode) string {
	return opt.ModeToWire(mode)
}
