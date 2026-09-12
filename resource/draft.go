package resource

import "sync"

// PlanGuardDraft is a package-neutral command probe for plan recording.
type PlanGuardDraft struct {
	Bin          string
	Args         []string
	ExpectStdout string
	ExpectExit   *int
}

// PlanDraft is a package-neutral snapshot of a registered resource for plan
// recording. The api package converts drafts to plan.Op so resource packages
// do not import plan (avoids cycles with plan apply helpers).
type PlanDraft struct {
	Kind string

	ID   string
	Path string

	Symlink  string
	Hardlink string

	Mode       string
	FileMode   string
	ContentB64 string
	Blob       string
	Prune      bool
	Absent     bool

	AddLine    string
	RemoveLine string

	Name    string
	Bin     string
	Args    []string
	Dir     string
	Env     map[string]string
	Creates string
	Unless  *PlanGuardDraft
	OnlyIf  *PlanGuardDraft
}

var (
	draftMu       sync.Mutex
	draftRecorder func(PlanDraft)
)

// SetPlanDraftRecorder installs the sink used by RecordPlanDraft.
// Pass nil to disable recording.
func SetPlanDraftRecorder(fn func(PlanDraft)) {
	draftMu.Lock()
	defer draftMu.Unlock()
	draftRecorder = fn
}

// PlanDraftRecording reports whether a draft recorder is installed.
func PlanDraftRecording() bool {
	draftMu.Lock()
	defer draftMu.Unlock()
	return draftRecorder != nil
}

// RecordPlanDraft forwards draft to the installed recorder when present.
func RecordPlanDraft(draft PlanDraft) {
	draftMu.Lock()
	fn := draftRecorder
	draftMu.Unlock()
	if fn != nil {
		fn(draft)
	}
}
