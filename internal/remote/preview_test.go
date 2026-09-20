package remote

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

func TestRequireRemoteGonfRefusesMissingAndStaleRuntimes(t *testing.T) {
	tests := []struct {
		name           string
		planVersion    int
		previewVersion int
		release        string
		want           string
	}{
		{"missing schema", 0, internal.StrictPreviewVersion, internal.Version, "does not install or update"},
		{"missing capability", plan.CurrentVersion, 0, internal.Version, "strict-preview capability"},
		{"stale release", plan.CurrentVersion, internal.StrictPreviewVersion, "0.0.0", "older than controller"},
		{"missing release", plan.CurrentVersion, internal.StrictPreviewVersion, "", "did not report a release version"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPusher()
			p.PlanVersionProber = func(context.Context, PushTarget) (int, error) {
				return tc.planVersion, nil
			}
			p.ReleaseVersionProber = func(context.Context, PushTarget) (string, error) {
				return tc.release, nil
			}
			p.StrictPreviewProber = func(context.Context, PushTarget) (int, error) {
				return tc.previewVersion, nil
			}

			err := p.RequireRemoteGonf(context.Background(), PushTarget{Host: "preview.example"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RequireRemoteGonf() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPreviewChunksNeverBootstrapsAndUsesStrictDryRun(t *testing.T) {
	oldPlan := defaultPusher.PlanVersionProber
	oldStrictPreview := defaultPusher.StrictPreviewProber
	oldRelease := defaultPusher.ReleaseVersionProber
	oldBuild := defaultPusher.GoBuildRunner
	oldSCP := defaultPusher.SCPRunner
	oldSSH := SSHRunner
	t.Cleanup(func() {
		defaultPusher.PlanVersionProber = oldPlan
		defaultPusher.StrictPreviewProber = oldStrictPreview
		defaultPusher.ReleaseVersionProber = oldRelease
		defaultPusher.GoBuildRunner = oldBuild
		defaultPusher.SCPRunner = oldSCP
		SSHRunner = oldSSH
	})

	defaultPusher.PlanVersionProber = func(context.Context, PushTarget) (int, error) {
		return plan.CurrentVersion, nil
	}
	defaultPusher.ReleaseVersionProber = func(context.Context, PushTarget) (string, error) {
		return internal.Version, nil
	}
	defaultPusher.StrictPreviewProber = func(context.Context, PushTarget) (int, error) {
		return internal.StrictPreviewVersion, nil
	}
	defaultPusher.GoBuildRunner = func(context.Context, string, string, string, string) error {
		t.Fatal("strict preview must not build gonf")
		return nil
	}
	defaultPusher.SCPRunner = func(context.Context, string, PushTarget, string) error {
		t.Fatal("strict preview must not copy gonf")
		return nil
	}

	var remoteCmd string
	SSHRunner = func(_ context.Context, stdin io.Reader, argv []string) error {
		remoteCmd = argv[len(argv)-1]
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	ops := []plan.Op{{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "preview"}}
	if err := PreviewChunks(context.Background(), PushTarget{Host: "preview.example"}, "preview", ops, nil); err != nil {
		t.Fatalf("PreviewChunks() = %v", err)
	}
	if remoteCmd != "gonf apply -n -strict-preview -" {
		t.Fatalf("remote command = %q", remoteCmd)
	}
}

func TestPreviewChunksProbesEveryAppliedPrivilegeContext(t *testing.T) {
	oldPlan := defaultPusher.PlanVersionProber
	oldStrictPreview := defaultPusher.StrictPreviewProber
	oldRelease := defaultPusher.ReleaseVersionProber
	oldSSH := SSHRunner
	t.Cleanup(func() {
		defaultPusher.PlanVersionProber = oldPlan
		defaultPusher.StrictPreviewProber = oldStrictPreview
		defaultPusher.ReleaseVersionProber = oldRelease
		SSHRunner = oldSSH
	})

	var probedUnprivileged, probedElevated bool
	defaultPusher.PlanVersionProber = func(_ context.Context, target PushTarget) (int, error) {
		if target.probeElevated {
			probedElevated = true
		} else {
			probedUnprivileged = true
		}
		return plan.CurrentVersion, nil
	}
	defaultPusher.ReleaseVersionProber = func(_ context.Context, target PushTarget) (string, error) {
		if target.probeElevated {
			probedElevated = true
		} else {
			probedUnprivileged = true
		}
		return internal.Version, nil
	}
	defaultPusher.StrictPreviewProber = func(_ context.Context, target PushTarget) (int, error) {
		if target.probeElevated {
			probedElevated = true
		} else {
			probedUnprivileged = true
		}
		return internal.StrictPreviewVersion, nil
	}
	SSHRunner = func(_ context.Context, stdin io.Reader, argv []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "preview"},
		{Op: plan.KindCommand, ID: "unprivileged", Bin: "true"},
		{Op: plan.KindCommand, ID: "elevated", Bin: "true", Elevate: true},
	}
	if err := PreviewChunks(context.Background(), PushTarget{Host: "preview.example", Privilege: privilege.Sudo}, "preview", ops, nil); err != nil {
		t.Fatalf("PreviewChunks() = %v", err)
	}
	if !probedUnprivileged || !probedElevated {
		t.Fatalf("probed unprivileged=%v elevated=%v, want both privilege contexts", probedUnprivileged, probedElevated)
	}
}

func TestPreviewChunksRefusesBlobsBeforeAnyRemoteProbe(t *testing.T) {
	mem := plan.NewMemoryStore()
	if _, err := mem.WriteFile("preview.txt", []byte("secret")); err != nil {
		t.Fatal(err)
	}

	oldPlan := defaultPusher.PlanVersionProber
	oldStrictPreview := defaultPusher.StrictPreviewProber
	oldSSH := SSHRunner
	t.Cleanup(func() {
		defaultPusher.PlanVersionProber = oldPlan
		defaultPusher.StrictPreviewProber = oldStrictPreview
		SSHRunner = oldSSH
	})
	defaultPusher.PlanVersionProber = func(context.Context, PushTarget) (int, error) {
		t.Fatal("blob-backed strict preview must not probe a host")
		return 0, nil
	}
	SSHRunner = func(context.Context, io.Reader, []string) error {
		t.Fatal("blob-backed strict preview must not open ssh")
		return nil
	}

	ops := []plan.Op{{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "preview"}}
	err := PreviewChunks(context.Background(), PushTarget{Host: "preview.example"}, "preview", ops, mem)
	if err == nil || !strings.Contains(err.Error(), "has blobs") {
		t.Fatalf("PreviewChunks() = %v, want blob refusal", err)
	}
}

func TestProbeStrictPreviewVersion(t *testing.T) {
	oldCapture := sshCaptureExec
	t.Cleanup(func() { sshCaptureExec = oldCapture })

	sshCaptureExec = func(context.Context, []string) (string, string, error) {
		return strconv.Itoa(internal.StrictPreviewVersion) + "\n", "", nil
	}
	got, err := probeStrictPreviewVersion(context.Background(), PushTarget{Host: "preview.example"})
	if err != nil || got != internal.StrictPreviewVersion {
		t.Fatalf("probeStrictPreviewVersion() = (%d, %v)", got, err)
	}

	sshCaptureExec = func(context.Context, []string) (string, string, error) {
		return "unsupported\n", "", nil
	}
	if _, err := probeStrictPreviewVersion(context.Background(), PushTarget{Host: "preview.example"}); err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("unparseable strict-preview probe error = %v", err)
	}
}
