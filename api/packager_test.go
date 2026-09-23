package api

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// bigSourceFile writes a file one byte over plan.MaxInlineContent, so packaging
// it needs a blob instead of inline content_b64.
func bigSourceFile(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, make([]byte, plan.MaxInlineContent+1), 0o600); err != nil {
		t.Fatal(err)
	}
	return src
}

// onlyDraft returns the single registered plan draft.
func onlyDraft(t *testing.T) resource.PlanDraft {
	t.Helper()
	drafts := resource.RegisteredPlanDrafts()
	if len(drafts) != 1 {
		t.Fatalf("draft count = %d, want 1", len(drafts))
	}
	return drafts[0]
}

// TestPackageApplyOpsIgnoresAndKeepsRecordingSession pins that api.Apply's
// packaging pass neither reads nor mutates the recording session: a stale
// elevate flag, task name and a claimed blob ref for the very ref this draft
// produces (under a different identity) do not elevate, mislabel or collide
// with the Apply's op, and the session is left exactly as it was.
func TestPackageApplyOpsIgnoresAndKeepsRecordingSession(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	InstallFile(filepath.Join(t.TempDir(), "dst"), bigSourceFile(t))
	draft := onlyDraft(t)
	ref, err := plan.BlobRefFor(blobName(draft))
	if err != nil {
		t.Fatal(err)
	}

	recSession.recordingElevate = true
	recSession.recordingStack = []string{"stale_task"}
	recSession.recordedBlobRefs = map[string]string{ref: "File[/someone/else]"}
	before := recSession

	ops, err := packageApplyOps([]resource.PlanDraft{draft}, plan.NewMemoryStore())
	if err != nil {
		t.Fatalf("packageApplyOps() = %v, want no stale blob-ref collision", err)
	}
	if len(ops) != 2 || ops[1].Blob != ref || ops[1].Elevate {
		t.Fatalf("ops = %#v, want header plus one unelevated op with blob %q", ops, ref)
	}
	if !reflect.DeepEqual(recSession, before) {
		t.Fatalf("recording session changed by packageApplyOps: %#v, want %#v", recSession, before)
	}
	// before shares the map, so check its contents too: no claim leaked in.
	if want := map[string]string{ref: "File[/someone/else]"}; !reflect.DeepEqual(recSession.recordedBlobRefs, want) {
		t.Fatalf("session blob refs = %v, want %v", recSession.recordedBlobRefs, want)
	}
}

// TestRecordingSessionPackagerCarriesLiveState pins the session-side seam:
// its packager folds in the current elevate flag and innermost task name, and
// claims blob refs in the session-wide map, so a claim through one packager is
// seen by the next.
func TestRecordingSessionPackagerCarriesLiveState(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	var s recordingSession
	if p := s.packager(nil); p.elevate || p.task != "" || p.blobRefs == nil {
		t.Fatalf("zero session packager = %#v, want no elevation, no task and a claimed-ref map", p)
	}
	s.reset()
	s.recordingElevate = true
	s.recordingStack = []string{"outer", "inner"}
	store := plan.NewMemoryStore()
	p := s.packager(store)
	if !p.elevate || p.task != "inner" || p.store != store {
		t.Fatalf("packager = %#v, want elevate, task inner and the given store", p)
	}
	if err := p.guardBlobRef("x", resource.PlanDraft{ID: "File[/a]"}); err != nil {
		t.Fatal(err)
	}
	if err := s.packager(store).guardBlobRef("x", resource.PlanDraft{ID: "File[/b]"}); err == nil {
		t.Fatal("second packager accepted a ref the session already claimed for another identity")
	}
}

// TestDraftPackagerGuardBlobRef covers the collision guard: a ref reused by
// the same identity is fine, by a different one is refused without taking
// the ref over.
func TestDraftPackagerGuardBlobRef(t *testing.T) {
	p := newDraftPackager(nil)
	a := resource.PlanDraft{ID: "File[/a]"}
	if err := p.guardBlobRef("conf", a); err != nil {
		t.Fatalf("first claim = %v", err)
	}
	if err := p.guardBlobRef("conf", a); err != nil {
		t.Fatalf("same identity reclaim = %v, want nil", err)
	}
	err := p.guardBlobRef("conf", resource.PlanDraft{ID: "File[/b]"})
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("different identity claim = %v, want a collision error", err)
	}
	// The refused claim must not take the ref over from its first owner.
	if want := map[string]string{"blobs/conf": "File[/a]"}; !reflect.DeepEqual(p.blobRefs, want) {
		t.Fatalf("claimed refs = %v, want %v", p.blobRefs, want)
	}
}

// TestDraftPackagerDraftError pins both message forms and the %w wrap.
func TestDraftPackagerDraftError(t *testing.T) {
	cause := errors.New("bad")
	d := resource.PlanDraft{ID: "User[x]"}
	tests := []struct {
		task, want string
	}{
		{"", `RecordPlan: draft "User[x]": bad`},
		{"svc", `RecordPlan: task "svc": draft "User[x]": bad`},
	}
	for _, tc := range tests {
		err := draftPackager{task: tc.task}.draftError(d, cause)
		if err.Error() != tc.want || !errors.Is(err, cause) {
			t.Errorf("task %q: draftError = %v, want %q wrapping the cause", tc.task, err, tc.want)
		}
	}
}

// TestDraftPackagerNeedsStoreForBlobs covers the store-less refusals: a file
// over the inline limit, a sync_dir glob and a sync_dir tree all need a blob
// store, while a small file packages inline without one.
func TestDraftPackagerNeedsStoreForBlobs(t *testing.T) {
	srcDir := t.TempDir()
	small := filepath.Join(srcDir, "small.conf")
	if err := os.WriteFile(small, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := bigSourceFile(t)
	dst := t.TempDir()
	tests := []struct {
		name     string
		register func()
		wantErr  string
	}{
		{"small file inline", func() { InstallFile(filepath.Join(dst, "s"), small) }, ""},
		{"big file", func() { InstallFile(filepath.Join(dst, "b"), big) }, "exceeds inline limit"},
		{"glob", func() { SyncDir(filepath.Join(dst, "g"), filepath.Join(srcDir, "*.conf")) }, "plan dir required"},
		{"tree", func() { Dir(filepath.Join(dst, "t"), options.WithSource(srcDir)) }, "plan dir required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ResetForTest()
			t.Cleanup(ResetForTest)
			tc.register()
			op, err := newDraftPackager(nil).packageDraft(onlyDraft(t))
			if tc.wantErr == "" {
				if err != nil || op.ContentB64 == "" || op.Blob != "" {
					t.Fatalf("packageDraft() = %#v, %v, want inline content", op, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("packageDraft() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// leakingPayload implements both resource.SourceFilePayload and
// resource.SourceDirPayload the way a FUTURE resource kind's payload could
// by accident: a like-named accessor added for that kind's own, unrelated
// purpose (see the task 0e2 annotation's reproduction, which added exactly
// such a method to cron.Payload and watched every recorded cron op silently
// gain the target file's bytes). It exists only so
// TestSourceAccessorsGatedOnKind can drive sourceFilePath/syncDirSource with
// a payload that WOULD leak if the d.Kind gate were missing or removed.
type leakingPayload struct{ path, dir, glob string }

func (leakingPayload) Clone() resource.DraftPayload      { return leakingPayload{} }
func (p leakingPayload) SourceFilePath() string          { return p.path }
func (p leakingPayload) SourceDirGlob() (string, string) { return p.dir, p.glob }

// TestSourceAccessorsGatedOnKind reproduces and closes the task 0e2 leak:
// before the fix, sourceFilePath/syncDirSource type-asserted
// resource.SourceFilePayload/resource.SourceDirPayload against whatever
// concrete type a draft's Payload held, with no check that the draft's Kind
// was ever meant to carry a source, so leakingPayload's bytes would have
// been packaged into ops of every kind below. It also proves the gate is
// purely additive-restrictive: the very same leakingPayload still answers
// correctly once the draft's Kind is one that legitimately carries a
// source, so the fix does not depend on the payload's identity, only on
// d.Kind.
func TestSourceAccessorsGatedOnKind(t *testing.T) {
	leaking := leakingPayload{path: "/etc/shadow", dir: "/etc", glob: "*.conf"}

	for _, kind := range []string{"cron", "command", "package", "service", "user", "timer", "", "widget"} {
		d := resource.PlanDraft{Kind: kind, Payload: leaking}
		if got := sourceFilePath(d); got != "" {
			t.Errorf("kind %q: sourceFilePath = %q, want \"\" (SourceFilePath must not be consulted for this kind)", kind, got)
		}
		if gotDir, gotGlob := syncDirSource(d); gotDir != "" || gotGlob != "" {
			t.Errorf("kind %q: syncDirSource = (%q, %q), want (\"\", \"\") (SourceDirGlob must not be consulted for this kind)", kind, gotDir, gotGlob)
		}
	}

	for _, kind := range []string{"file", "ensure_file"} {
		d := resource.PlanDraft{Kind: kind, Payload: leaking}
		if got := sourceFilePath(d); got != leaking.path {
			t.Errorf("kind %q: sourceFilePath = %q, want %q", kind, got, leaking.path)
		}
	}
	d := resource.PlanDraft{Kind: "sync_dir", Payload: leaking}
	if gotDir, gotGlob := syncDirSource(d); gotDir != leaking.dir || gotGlob != leaking.glob {
		t.Errorf("sync_dir: syncDirSource = (%q, %q), want (%q, %q)", gotDir, gotGlob, leaking.dir, leaking.glob)
	}
}
