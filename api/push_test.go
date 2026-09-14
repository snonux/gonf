package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// sshCall captures one fake remote.SSHRunner invocation.
type sshCall struct {
	argv   []string
	remote string // last argv element: the remote shell command
	stdin  []byte // payload streamed to ssh
}

// captureSSH installs a fake remote.SSHRunner recording every invocation.
func captureSSH(t *testing.T) *[]sshCall {
	t.Helper()
	old := remote.SSHRunner
	t.Cleanup(func() { remote.SSHRunner = old })
	calls := &[]sshCall{}
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stdin)
		*calls = append(*calls, sshCall{
			argv:   append([]string(nil), argv...),
			remote: argv[len(argv)-1],
			stdin:  buf.Bytes(),
		})
		return nil
	}
	return calls
}

// recordSyncDirTasks registers one privileged and one unprivileged task, both
// syncing a small tree so each plan chunk references a blob.
func recordSyncDirTasks(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	srcDir := filepath.Join(base, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.conf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	Task("root_sync", "", func() {
		SyncDir(filepath.Join(base, "root.dst"), filepath.Join(srcDir, "*"))
	}, Privileged())
	Task("user_sync", "", func() {
		SyncDir(filepath.Join(base, "user.dst"), filepath.Join(srcDir, "*"))
	})
}

// assertChunkBlobRefs decodes a plan-only push frame and checks that every
// blob referenced by its ops resolves inside planDir (the sticky dir the
// unprivileged blob upload populated).
func assertChunkBlobRefs(t *testing.T, name string, stdin []byte, planDir string) {
	t.Helper()
	payload, err := plan.DecodePush(bytes.NewReader(stdin), "")
	if err != nil {
		t.Fatalf("%s: decode: %v", name, err)
	}
	for _, op := range payload.Ops {
		if op.Blob == "" {
			continue
		}
		if _, err := plan.Resolve(planDir, op.Blob); err != nil {
			t.Fatalf("%s: blob %q not readable by unprivileged apply: %v", name, op.Blob, err)
		}
	}
}

// Pushing a plan with Privileged() tasks and -privilege=none must fail
// before any SSH traffic: no chunk, and no blob upload either, in both
// chunk orderings (pre-flight of the remote commands).
func TestPushPrivilegeNoneFailsBeforeAnySSH(t *testing.T) {
	for _, tasks := range [][]string{{"root_sync", "user_sync"}, {"user_sync", "root_sync"}} {
		ResetTasks()
		ResetInventory()
		resource.ResetRepository()
		recordSyncDirTasks(t)

		calls := captureSSH(t)
		err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.None}, "demo", tasks...)
		if err == nil || !strings.Contains(err.Error(), "-privilege=none") {
			t.Fatalf("tasks=%v: want privilege error, got %v", tasks, err)
		}
		if len(*calls) != 0 {
			t.Fatalf("tasks=%v: ssh calls on error: %v", tasks, remotes(*calls))
		}
	}
}

// PushPayload rejects elevated payloads with -privilege=none before any
// SSH call as well.
func TestPushPayloadPrivilegeNoneElevateFailsBeforeSSH(t *testing.T) {
	calls := captureSSH(t)
	err := PushPayload(PushTarget{Host: "h.example", Privilege: privilege.None}, []byte("GONF-PUSH/1"), true, "")
	if err == nil || !strings.Contains(err.Error(), "-privilege=none") {
		t.Fatalf("want privilege error, got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("ssh calls on error: %v", remotes(*calls))
	}
}

// A privilege split with blobs must upload blobs in a dedicated
// non-elevated session before any apply chunk: when the first chunk is
// elevated, embedded extraction would run under sudo/doas and leave the
// sticky blobs root-owned, breaking later unprivileged chunks.
func TestPushUploadsBlobsBeforeElevatedChunks(t *testing.T) {
	ResetTasks()
	ResetInventory()
	resource.ResetRepository()
	recordSyncDirTasks(t)

	calls := captureSSH(t)
	err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.Doas}, "demo", "root_sync", "user_sync")
	if err != nil {
		t.Fatal(err)
	}

	sticky := "/tmp/gonf-apply-sticky-demo"
	// 4 sessions: blob upload, two apply chunks, then the sticky-dir removal.
	if len(*calls) != 4 {
		t.Fatalf("calls=%d remotes=%v", len(*calls), remotes(*calls))
	}
	// Session 0: blob upload, plain apply, never wrapped.
	if got := (*calls)[0].remote; got != "gonf apply -apply-dir "+sticky+" -" {
		t.Fatalf("blob upload remote=%q", got)
	}
	if strings.Contains((*calls)[0].remote, "sudo") || strings.Contains((*calls)[0].remote, "doas") {
		t.Fatalf("blob upload must not be privilege-wrapped: %q", (*calls)[0].remote)
	}
	// Session 1/2: apply chunks carry -apply-dir; only the elevated chunk is
	// wrapped.
	if got := (*calls)[1].remote; got != "doas gonf apply -apply-dir "+sticky+" -" {
		t.Fatalf("elevated chunk remote=%q", got)
	}
	if got := (*calls)[2].remote; got != "gonf apply -apply-dir "+sticky+" -" {
		t.Fatalf("unprivileged chunk remote=%q", got)
	}

	// Blob session: GONF-PUSH/1 with blobs attached and a header-only plan.
	if !strings.HasPrefix(string((*calls)[0].stdin), "GONF-PUSH/1\nblobs 1\n") {
		t.Fatalf("blob upload missing blobs frame: %q", (*calls)[0].stdin[:min(40, len((*calls)[0].stdin))])
	}
	uploaded, err := plan.DecodePush(bytes.NewReader((*calls)[0].stdin), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.PlanDir == "" {
		t.Fatal("blob upload carried no blobs")
	}
	if len(uploaded.Ops) != 1 || uploaded.Ops[0].Op != plan.KindPlan {
		t.Fatalf("blob upload plan must be header-only, got %#v", uploaded.Ops)
	}

	// Apply chunks: plan-only frames; every referenced blob must be readable
	// from the login-user-owned sticky dir afterwards.
	for i, wantElevate := range []bool{true, false} {
		stdin := (*calls)[i+1].stdin
		if !strings.HasPrefix(string(stdin), "GONF-PUSH/1\nblobs 0\n") {
			t.Fatalf("chunk %d must not carry blobs: %q", i, stdin[:min(40, len(stdin))])
		}
		payload, err := plan.DecodePush(bytes.NewReader(stdin), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(payload.Ops) < 2 || payload.Ops[0].Op != plan.KindPlan {
			t.Fatalf("chunk %d ops=%#v", i, payload.Ops)
		}
		for _, op := range payload.Ops[1:] {
			if op.Elevate != wantElevate {
				t.Fatalf("chunk %d op elevate=%v want %v: %#v", i, op.Elevate, wantElevate, op)
			}
		}
		assertChunkBlobRefs(t, "chunk", stdin, uploaded.PlanDir)
	}
}

// With an unprivileged chunk first the protocol is identical: blobs still go
// first in their own session and no chunk carries them.
func TestPushUploadsBlobsBeforeUnprivilegedFirstChunks(t *testing.T) {
	ResetTasks()
	ResetInventory()
	resource.ResetRepository()
	recordSyncDirTasks(t)

	calls := captureSSH(t)
	err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.Doas}, "demo", "user_sync", "root_sync")
	if err != nil {
		t.Fatal(err)
	}

	sticky := "/tmp/gonf-apply-sticky-demo"
	// 4 sessions: blob upload, two apply chunks, then the sticky-dir removal.
	if len(*calls) != 4 {
		t.Fatalf("calls=%d remotes=%v", len(*calls), remotes(*calls))
	}
	if got := (*calls)[0].remote; got != "gonf apply -apply-dir "+sticky+" -" {
		t.Fatalf("blob upload remote=%q", got)
	}
	if got := (*calls)[1].remote; got != "gonf apply -apply-dir "+sticky+" -" {
		t.Fatalf("unprivileged chunk remote=%q", got)
	}
	if got := (*calls)[2].remote; got != "doas gonf apply -apply-dir "+sticky+" -" {
		t.Fatalf("elevated chunk remote=%q", got)
	}
	if strings.Contains((*calls)[0].remote, "sudo") || strings.Contains((*calls)[0].remote, "doas") {
		t.Fatalf("blob upload must not be privilege-wrapped: %q", (*calls)[0].remote)
	}
}

// A single-chunk plan keeps blobs embedded in its one apply stream: no
// pre-step, no sticky dir, no -apply-dir.
func TestPushSingleChunkKeepsEmbeddedBlobs(t *testing.T) {
	ResetTasks()
	ResetInventory()
	resource.ResetRepository()
	base := t.TempDir()
	srcDir := filepath.Join(base, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.conf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	Task("user_sync", "", func() {
		SyncDir(filepath.Join(base, "user.dst"), filepath.Join(srcDir, "*"))
	})

	calls := captureSSH(t)
	err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.Doas}, "solo", "user_sync")
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls=%d remotes=%v", len(*calls), remotes(*calls))
	}
	if got := (*calls)[0].remote; got != "gonf apply -" {
		t.Fatalf("remote=%q want plain apply without -apply-dir", got)
	}
	if !strings.HasPrefix(string((*calls)[0].stdin), "GONF-PUSH/1\nblobs 1\n") {
		t.Fatal("single-chunk push must keep blobs embedded")
	}
	payload, err := plan.DecodePush(bytes.NewReader((*calls)[0].stdin), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Ops) != 2 {
		t.Fatalf("ops=%#v", payload.Ops)
	}
}

func remotes(calls []sshCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.remote)
	}
	return out
}

// TestPushRemovesStickyDirAfterLastChunk pins task 412: after the last apply
// chunk succeeds, the controller removes the remote sticky apply dir (one
// unprivileged rm -rf), so blob staging does not accumulate under /tmp.
func TestPushRemovesStickyDirAfterLastChunk(t *testing.T) {
	ResetTasks()
	ResetInventory()
	resource.ResetRepository()
	recordSyncDirTasks(t)

	calls := captureSSH(t)
	if err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.Doas}, "demo", "root_sync", "user_sync"); err != nil {
		t.Fatal(err)
	}

	sticky := "/tmp/gonf-apply-sticky-demo"
	// 3 apply sessions + exactly one final rm -rf.
	if len(*calls) != 4 {
		t.Fatalf("calls=%d remotes=%v", len(*calls), remotes(*calls))
	}
	removals := 0
	for _, c := range *calls {
		if c.remote == "rm -rf "+sticky {
			removals++
		}
	}
	if removals != 1 {
		t.Errorf("expected exactly one sticky-dir removal, got %d: %v", removals, remotes(*calls))
	}
	// The removal must be the LAST session (after all applies).
	if got := (*calls)[3].remote; got != "rm -rf "+sticky {
		t.Fatalf("last remote=%q, want the sticky-dir removal", got)
	}
	// The removal must not be privilege-wrapped (login user owns the dir).
	if strings.Contains((*calls)[3].remote, "doas") || strings.Contains((*calls)[3].remote, "sudo") {
		t.Fatalf("sticky removal must not be privilege-wrapped: %q", (*calls)[3].remote)
	}
}

// TestPushRemovesStickyDirOnChunkFailure pins the best-effort cleanup on the
// failure path: a failing chunk must not leak the sticky dir, and the error
// must report how much of the plan already applied.
func TestPushRemovesStickyDirOnChunkFailure(t *testing.T) {
	ResetTasks()
	ResetInventory()
	resource.ResetRepository()
	recordSyncDirTasks(t)

	calls := captureSSH(t)
	// Fail the LAST chunk (the unprivileged apply session): chunk 2 fails
	// after earlier chunks already applied, which must be reported.
	old := remote.SSHRunner
	remote.SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stdin)
		payload := buf.Bytes()
		// Fail only the LAST chunk: the unprivileged apply session whose
		// frame carries no blobs (the blob upload session has "blobs 1", the
		// elevated chunk is doas-wrapped).
		isLastChunk := !strings.Contains(argv[len(argv)-1], "doas") &&
			strings.HasPrefix(string(payload), "GONF-PUSH/1\nblobs 0\n")
		if isLastChunk {
			return fmt.Errorf("remote refused")
		}
		*calls = append(*calls, sshCall{argv: append([]string(nil), argv...), remote: argv[len(argv)-1], stdin: payload})
		return nil
	}
	t.Cleanup(func() { remote.SSHRunner = old })

	err := PushTo(PushTarget{Host: "h.example", Privilege: privilege.Doas}, "demo", "root_sync", "user_sync")
	if err == nil {
		t.Fatal("expected the push to fail when the elevated chunk fails")
	}
	if !strings.Contains(err.Error(), "host left partially applied") {
		t.Errorf("error should report the partial apply: %v", err)
	}
	// The sticky dir removal must still have been attempted (recorded as the
	// final rm session).
	removals := 0
	for _, c := range *calls {
		if c.remote == "rm -rf /tmp/gonf-apply-sticky-demo" {
			removals++
		}
	}
	if removals != 1 {
		t.Errorf("expected exactly one sticky-dir removal attempt, got %d: %v", removals, remotes(*calls))
	}
}
