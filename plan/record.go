package plan

import (
	"fmt"
	"os"
	"sync"
)

var (
	recordMu  sync.Mutex
	recording bool
	recorded  []Op
)

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

// FormatMode formats a permission mask as an octal string (e.g. "0640").
func FormatMode(mode os.FileMode) string {
	return fmt.Sprintf("%#o", mode&os.ModePerm)
}
