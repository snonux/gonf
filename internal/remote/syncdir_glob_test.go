package remote

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/plan"
)

// TestRemoteBeforeSyncDirGlobSchemaIsRefused is the sb2 version-skew guard: a
// remote that reports the pre-glob schema (22) would rebuild a glob sync_dir
// as a tree sync and prune unmanaged subdirectories of the destination. Push
// upgrades such a remote before applying (EnsureRemoteGonf, covered by
// TestRemoteBeforeSensitiveSchemaIsRefusedOrUpgraded); strict preview, which
// installs nothing, must refuse it by name.
func TestRemoteBeforeSyncDirGlobSchemaIsRefused(t *testing.T) {
	old := plan.VersionSyncDirGlob - 1
	p := NewPusher()
	p.PlanVersionProber = func(context.Context, PushTarget, ProbeContext) (int, error) { return old, nil }
	p.ReleaseVersionProber = func(context.Context, PushTarget, ProbeContext) (string, error) { return internal.Version, nil }
	p.StrictPreviewProber = func(context.Context, PushTarget, ProbeContext) (int, error) {
		return internal.StrictPreviewVersion, nil
	}
	err := p.RequireRemoteGonf(context.Background(), PushTarget{Host: "preview.example"}, ProbeLogin)
	want := fmt.Sprintf("schema %d is older than controller schema %d", old, plan.CurrentVersion)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("RequireRemoteGonf(v%d) = %v, want a refusal naming %q", old, err, want)
	}
}
