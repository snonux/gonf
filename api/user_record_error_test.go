package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// TestUserRecordErrorNamesTaskAndDraft pins task 472's record-time rejection
// wording: it names the task and the draft's resource ID, like the other
// record-time draft errors, and the user reason follows once.
func TestUserRecordErrorNamesTaskAndDraft(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("svc_bad", "", func() { User("-bad") })
	_, err := RecordPlanTo("bad", plan.NewMemoryStore(), "svc_bad")
	want := `RecordPlan: task "svc_bad": draft "User[-bad]": user name "-bad" starts with -`
	if err == nil || err.Error() != want {
		t.Fatalf("RecordPlanTo() = %v, want %q", err, want)
	}
}

// TestUserRecordErrorFiresInNeverAppliedTask pins the documented trade-off:
// the controller cannot know the destination, so a user no platform could
// apply fails recording even inside a task whose serializable guard would
// never match any destination.
func TestUserRecordErrorFiresInNeverAppliedTask(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("svc_guarded", "", func() { User("-bad") }, WhenProfile("no-such-profile"))
	if _, err := RecordPlanTo("guarded", plan.NewMemoryStore(), "svc_guarded"); err == nil {
		t.Fatal("RecordPlanTo() = nil, want the record-time user rejection")
	}
}

// TestDraftErrorWrapsTheHandlerError pins draftError's %w: callers can still
// match the handler's underlying error (here encoding/json's refusal of
// function-valued template data) with errors.As.
func TestDraftErrorWrapsTheHandlerError(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	path := filepath.Join(t.TempDir(), "config")
	Task("bad_data", "", func() {
		File(path, options.WithContent("x"), options.WithTemplateData(map[string]any{"bad": func() {}}))
	})
	_, err := RecordPlanTo("bad", plan.NewMemoryStore(), "bad_data")
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("RecordPlanTo() = %v, want it to wrap *json.UnsupportedTypeError", err)
	}
	if want := `RecordPlan: task "bad_data": draft "File[` + path + `]": `; !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("RecordPlanTo() = %v, want prefix %q", err, want)
	}
}

// TestPanickingTaskDoesNotLeakIntoLaterDraftErrors is the regression test for
// a recovered panic inside a recorded task body: its name must not stay on
// the recording stack and be blamed by a later local Apply's draft error.
func TestPanickingTaskDoesNotLeakIntoLaterDraftErrors(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("boom", "", func() { panic("boom") })
	func() {
		defer func() { _ = recover() }()
		_, _ = RecordPlanTo("boom", plan.NewMemoryStore(), "boom")
	}()
	if len(recSession.recordingStack) != 0 {
		t.Fatalf("recording stack after a recovered panic = %v, want empty", recSession.recordingStack)
	}
	// Simulate a stack leaked by any other path: Apply must not use it either.
	recSession.recordingStack = []string{"boom"}
	resource.SetPlanDraftRecorder(nil)
	plan.SetRecording(false)
	User("-bad")
	err := Apply()
	want := `RecordPlan: draft "User[-bad]": user name "-bad" starts with -`
	if err == nil || err.Error() != want {
		t.Fatalf("Apply() = %v, want %q (no stale task name)", err, want)
	}
}

// TestLocalApplyRejectsUserBeforeApplyingAnything pins that a local
// api.Apply lowers every draft before applying any, so an impossible user
// aborts the whole apply: the file registered before it is not written.
func TestLocalApplyRejectsUserBeforeApplyingAnything(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	path := filepath.Join(t.TempDir(), "written-only-if-applied")
	File(path, options.WithContent("x"))
	User("-bad")
	err := Apply()
	want := `RecordPlan: draft "User[-bad]": user name "-bad" starts with -`
	if err == nil || err.Error() != want {
		t.Fatalf("Apply() = %v, want %q", err, want)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("file was written before the rejection (stat: %v)", statErr)
	}
}
