package remote

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// fakeProbesByContext swaps defaultPusher's three probers (and SSHRunner) for
// fakes that record every ProbeContext they are called with and report the
// controller's versions — except a plan schema of 0 for the contexts in
// stale. It restores everything on cleanup and returns the recorded contexts.
func fakeProbesByContext(t *testing.T, stale ...ProbeContext) *[]ProbeContext {
	t.Helper()
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
	var seen []ProbeContext
	defaultPusher.PlanVersionProber = func(_ context.Context, _ PushTarget, pc ProbeContext) (int, error) {
		seen = append(seen, pc)
		for _, s := range stale {
			if s == pc {
				return 0, nil
			}
		}
		return plan.CurrentVersion, nil
	}
	defaultPusher.StrictPreviewProber = func(context.Context, PushTarget, ProbeContext) (int, error) {
		return internal.StrictPreviewVersion, nil
	}
	defaultPusher.ReleaseVersionProber = func(context.Context, PushTarget, ProbeContext) (string, error) {
		return internal.Version, nil
	}
	SSHRunner = func(_ context.Context, stdin io.Reader, _ []string) error {
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	return &seen
}

// TestPreviewDeliveryProbesEveryAppliedPrivilegeContext pins that strict
// preview probes exactly the privilege contexts its chunks will apply in —
// the login context for unprivileged chunks, the elevated one for elevated
// chunks, both for a mixed plan — and refuses when the context an elevated
// chunk needs reports a stale gonf even though the login context is current.
func TestPreviewDeliveryProbesEveryAppliedPrivilegeContext(t *testing.T) {
	header := plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "preview"}
	unpriv := plan.Op{Op: plan.KindCommand, ID: "unprivileged", Payload: plan.CommandPayload{Bin: "true"}}
	elev := plan.Op{Op: plan.KindCommand, ID: "elevated", Elevate: true, Payload: plan.CommandPayload{Bin: "true"}}
	tests := []struct {
		name    string
		ops     []plan.Op
		stale   []ProbeContext
		want    []ProbeContext
		wantErr string
	}{
		{"unprivileged only", []plan.Op{header, unpriv}, nil, []ProbeContext{ProbeLogin}, ""},
		{"elevated only", []plan.Op{header, elev}, nil, []ProbeContext{ProbeElevated}, ""},
		{"mixed", []plan.Op{header, unpriv, elev}, nil, []ProbeContext{ProbeLogin, ProbeElevated}, ""},
		{"stale elevated binary", []plan.Op{header, unpriv, elev}, []ProbeContext{ProbeElevated},
			[]ProbeContext{ProbeLogin, ProbeElevated}, "plan schema 0 is older"},
		{"stale login binary unused", []plan.Op{header, elev}, []ProbeContext{ProbeLogin},
			[]ProbeContext{ProbeElevated}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seen := fakeProbesByContext(t, tc.stale...)
			target := PushTarget{Host: "preview.example", Privilege: privilege.Sudo}
			err := previewToHost(context.Background(), target, "preview", tc.ops, nil)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("previewToHost() = %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("previewToHost() = %v, want %q", err, tc.wantErr)
			}
			if !reflect.DeepEqual(*seen, tc.want) {
				t.Fatalf("probed contexts = %v, want %v", *seen, tc.want)
			}
		})
	}
}

// TestEnsureRemoteGonfProbesLoginContext pins that the push bootstrap never
// probes elevated: it installs and verifies gonf for the SSH login user.
func TestEnsureRemoteGonfProbesLoginContext(t *testing.T) {
	p := NewPusher()
	var seen []ProbeContext
	p.PlanVersionProber = func(_ context.Context, _ PushTarget, pc ProbeContext) (int, error) {
		seen = append(seen, pc)
		return plan.CurrentVersion, nil
	}
	p.ReleaseVersionProber = func(_ context.Context, _ PushTarget, pc ProbeContext) (string, error) {
		seen = append(seen, pc)
		return internal.Version, nil
	}
	target := PushTarget{Host: "push.example", Privilege: privilege.Sudo}
	if _, err := p.EnsureRemoteGonf(context.Background(), target); err != nil {
		t.Fatalf("EnsureRemoteGonf() = %v", err)
	}
	if want := []ProbeContext{ProbeLogin, ProbeLogin}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("probed contexts = %v, want %v", seen, want)
	}
}

// TestRemoteProbeCmdWrapsOnlyElevatedContext pins how the probe context maps
// onto the remote command: bare for the login context, wrapped like an
// elevated apply chunk for the elevated one, and refused when the target has
// no privilege wrapper to elevate with.
func TestRemoteProbeCmdWrapsOnlyElevatedContext(t *testing.T) {
	tests := []struct {
		name    string
		mode    privilege.Mode
		pc      ProbeContext
		want    string
		wantErr bool
	}{
		{"login sudo", privilege.Sudo, ProbeLogin, "/opt/gonf -version", false},
		{"elevated sudo", privilege.Sudo, ProbeElevated, "sudo -n /opt/gonf -version", false},
		{"elevated doas", privilege.Doas, ProbeElevated, "doas /opt/gonf -version", false},
		{"login none", privilege.None, ProbeLogin, "/opt/gonf -version", false},
		{"elevated none", privilege.None, ProbeElevated, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := PushTarget{Host: "h", Privilege: tc.mode, GonfPath: "/opt/gonf"}
			got, err := remoteProbeCmd(target, tc.pc, "-version")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("remoteProbeCmd() = %q, %v, want %q (error %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
