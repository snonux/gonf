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
	// Target is the existence-checked path for link_if_exists drafts.
	Target string

	Mode       string
	FileMode   string
	ContentB64 string
	Blob       string
	// SourcePath is a controller-local file to package as content_b64 or a blob.
	SourcePath string
	// SourceDir is a controller-local directory to package as a blob tree.
	SourceDir string
	// SourceGlob is a controller-local glob to package as a flat blob dir.
	SourceGlob string
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

	// User selects systemd --user for timer / daemon_reload drafts.
	User bool
	// EnableOnly skips start/stop for timer present (enable/disable only).
	EnableOnly bool
	// IfChanged gates daemon_reload on watched dependency outcomes.
	IfChanged bool
	// Watch lists resource ids for IfChanged (usually DependsOn targets).
	Watch []string
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
