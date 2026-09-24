package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// sensitiveStickyOps is stickyOps with the elevated op marked sensitive and
// blob-backed, and the blob store holding its blob.
func sensitiveStickyOps(t *testing.T, elevate bool) ([]plan.Op, *plan.MemoryStore) {
	t.Helper()
	ops, mem := stickyOps(t)
	ops[2] = plan.Op{Op: plan.KindFile, ID: "File[/etc/secret.conf]", Path: "/etc/secret.conf", Mode: "0600",
		Blob: "blobs/demo.txt", Payload: plan.FilePayload{HasContent: true}, Sensitive: true, Elevate: elevate}
	if !elevate {
		ops[1].Elevate = true // keep two chunks: the elevated one now holds no sensitive op
	}
	return ops, mem
}

// Sealing is narrow: a sensitive blob in an unprivileged chunk (the login
// user owns that content anyway) still pushes through the sticky dir as
// plaintext.
func TestToHostAllowsSensitiveUnprivilegedStickyBlob(t *testing.T) {
	r := installDeliveryRecorder(t)
	ops, mem := sensitiveStickyOps(t, false)
	target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
	if err := pushToHost(context.Background(), target, "p", ops, mem); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(r.cmds()) == 0 {
		t.Fatal("push sent nothing")
	}
}

// Version skew: a remote that reports the pre-sensitivity schema (v0.15.0
// reports 21) cannot honour sensitive ops. Strict preview refuses it without
// installing anything; ordinary push upgrades it first.
func TestRemoteBeforeSensitiveSchemaIsRefusedOrUpgraded(t *testing.T) {
	old := plan.VersionSensitive - 1
	p := NewPusher()
	p.PlanVersionProber = func(context.Context, PushTarget, ProbeContext) (int, error) { return old, nil }
	p.ReleaseVersionProber = func(context.Context, PushTarget, ProbeContext) (string, error) { return internal.Version, nil }
	p.StrictPreviewProber = func(context.Context, PushTarget, ProbeContext) (int, error) {
		return internal.StrictPreviewVersion, nil
	}
	err := p.RequireRemoteGonf(context.Background(), PushTarget{Host: "preview.example"}, ProbeLogin)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("schema %d is older than controller schema %d", old, plan.CurrentVersion)) {
		t.Fatalf("RequireRemoteGonf(v%d) = %v, want a refusal", old, err)
	}

	oldSSH, oldCapture := SSHRunner, sshCaptureExec
	t.Cleanup(func() { SSHRunner, sshCaptureExec = oldSSH, oldCapture })
	SSHRunner = func(context.Context, io.Reader, []string) error { return nil }
	sshCaptureExec = func(_ context.Context, argv []string) (string, string, error) {
		if strings.HasPrefix(argv[len(argv)-1], "mktemp -d ") {
			return "/tmp/gonf-sync.sens0001\n", "", nil
		}
		if strings.Contains(argv[len(argv)-1], "-plan-version") {
			return strconv.Itoa(plan.CurrentVersion) + "\n", "", nil
		}
		return "", "", fmt.Errorf("unexpected remote command %q", argv[len(argv)-1])
	}
	up := newBuildTestPusher(t)
	up.PlanVersionProber = p.PlanVersionProber
	up.ReleaseVersionProber = p.ReleaseVersionProber
	up.GoBuildRunner = func(_ context.Context, _, _, out, _ string) error { return os.WriteFile(out, []byte("fake"), 0o755) }
	scpCalls := 0
	up.SCPRunner = func(context.Context, string, PushTarget, string) error { scpCalls++; return nil }
	installed, err := up.EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err != nil || installed == "" || scpCalls != 1 {
		t.Fatalf("EnsureRemoteGonf(v%d) = %q, %v (scp %d), want an upgrade", old, installed, err, scpCalls)
	}
}
