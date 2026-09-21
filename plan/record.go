package plan

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	opt "github.com/snonux/gonf/resource/options"
)

var (
	recordMu  sync.Mutex
	recording bool
	recorded  []Op
)

// Recording, the draft recorder (resource.SetPlanDraftRecorder), the draft
// amender (resource.SetPlanDraftAmender, which re-lowers an amended draft
// and calls AmendRecorded) and the api recording session form the
// record-mode set: RecordPlanTo (api/plan.go) always sets and clears all
// four together for one recording session. They live in separate packages
// because importing api from here would be a cycle; the relationship is
// documented here, in resource/draft.go and in api/plan.go.

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

// AmendRecorded replaces the most recently recorded op carrying id with
// amend(op). It exists for resources that are singletons per recipe scope
// (daemon-reload: one per systemd bus) and fold a later declaration into the
// op they already recorded instead of recording a second one.
//
// The amendment is only sound while the op is still part of the contiguous,
// same-privilege run of resource ops being recorded: the amended op may gain
// deps and watches on ops recorded after it, and apply reorders deps only
// within one run between control ops (sortRunByDeps), while change reports
// and dependency order never cross a privilege chunk (ValidateChunkDeps,
// ValidateChangeGates). AmendRecorded therefore refuses, without changing
// anything, when a control op (when_begin/when_end) or an op with a different
// elevate flag was recorded after the target, and when the amended op's
// elevate flag differs from the target's: the new declaration was lowered
// under another privilege (e.g. a Privileged nested Run registered the
// target and its unprivileged caller declared again), so the merged op
// would belong to neither chunk. It is a no-op returning nil
// when recording is disabled or no op carries id: nothing recorded means
// nothing on the plan can disagree with the amended resource.
func AmendRecorded(id string, amend func(Op) (Op, error)) error {
	recordMu.Lock()
	defer recordMu.Unlock()
	if !recording {
		return nil
	}
	at := -1
	for i := len(recorded) - 1; i >= 0; i-- {
		if recorded[i].ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return nil
	}
	if err := checkAmendableRun(recorded[at], recorded[at+1:]); err != nil {
		return err
	}
	op, err := amend(recorded[at])
	if err != nil {
		return err
	}
	if op.Elevate != recorded[at].Elevate {
		return fmt.Errorf("op %s was recorded with elevate=%v but the amending declaration has elevate=%v, so they belong to different privilege chunks", id, recorded[at].Elevate, op.Elevate)
	}
	recorded[at] = op
	return nil
}

// checkAmendableRun refuses an amendment of target when any op recorded
// after it closes or opens a when-block or belongs to another privilege
// chunk (see AmendRecorded).
func checkAmendableRun(target Op, after []Op) error {
	for _, op := range after {
		if IsControlKind(op.Op) {
			return fmt.Errorf("op %s was recorded before a %s boundary, so it cannot be ordered after resources recorded behind that boundary", target.ID, op.Op)
		}
		if op.Elevate != target.Elevate {
			return fmt.Errorf("op %s was recorded before op %s of another privilege chunk, so it cannot depend on or watch resources recorded after that boundary", target.ID, op.ID)
		}
	}
	return nil
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

// ParseMode is FormatMode's apply-side inverse: it parses an octal plan-wire
// mode string such as "0640" or "04755" into a Go FileMode. The special bits
// 0o4000/0o2000/0o1000 (setuid, setgid, sticky) are converted to the
// os.ModeSetuid/ModeSetgid/ModeSticky flag bits, because Go only honors them
// through those flags: a raw os.FileMode(0o4755) would have its high bits
// truncated by os.Chmod and lower to 0755. Bits above 0o7777 have no meaning
// in the plan wire format and are rejected loudly instead of being silently
// dropped. Every resource kind's plan.Handler.Apply parses a wire mode
// through this one function, so the two directions (FormatMode at record
// time, ParseMode at apply time) cannot drift apart.
func ParseMode(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid mode %q: %w", s, err)
	}
	if v > 0o7777 {
		return 0, fmt.Errorf("invalid mode %q: only setuid/setgid/sticky (0o4000/0o2000/0o1000) plus the nine permission bits (up to 0o7777) are supported", s)
	}
	return opt.ModeToFlags(os.FileMode(v)), nil
}
