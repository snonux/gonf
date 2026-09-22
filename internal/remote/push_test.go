package remote

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// TestRemoteApplyCmdPrivilegeNoneElevateErrors covers the remote wrapping
// decision: it must not depend on the controller's euid. -privilege=none with
// an elevated chunk is an error even when gonf itself runs as root. Previously
// the root controller silently sent a plain `gonf apply -` to the remote,
// under-applying on non-root SSH logins.
func TestRemoteApplyCmdPrivilegeNoneElevateErrors(t *testing.T) {
	tests := []struct {
		name    string
		mode    privilege.Mode
		elevate bool
		want    string
		wantErr bool
	}{
		{"none_plain", privilege.None, false, "gonf apply -", false},
		{"none_elevate", privilege.None, true, "", true},
		{"sudo_elevate", privilege.Sudo, true, "sudo -n gonf apply -", false},
		{"doas_elevate", privilege.Doas, true, "doas gonf apply -", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remoteApplyCmd(tc.elevate, PushTarget{Privilege: tc.mode}, "", Push)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "-privilege=none") {
					t.Fatalf("want privilege error, got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// firstConnectTimeout returns the first ConnectTimeout option in an ssh argv:
// ssh uses the first occurrence on the command line, so this is the value in
// effect.
func firstConnectTimeout(argv []string) string {
	for _, a := range argv {
		if strings.HasPrefix(a, "ConnectTimeout=") {
			return a
		}
	}
	return ""
}

// Every generated ssh argv must bound the handshake with -o ConnectTimeout:
// a half-open connection (dropped firewall state, wedged host) would
// otherwise hang the push forever. The remote apply itself is deliberately
// not bounded by it — applies are long by nature.
func TestSSHArgvConnectTimeout(t *testing.T) {
	argv := PushTarget{Host: "h.example"}.sshArgv("gonf apply -")
	if got := firstConnectTimeout(argv); got != "ConnectTimeout=15" {
		t.Fatalf("argv=%v: first ConnectTimeout=%q, want ConnectTimeout=15", argv, got)
	}
}

// An explicit ConnectTimeout from ExtraSSH (or -- ssh-args) must win over the
// default: ssh uses the first option on the command line, and ExtraSSH comes
// first.
func TestSSHArgvConnectTimeoutOverride(t *testing.T) {
	targ := PushTarget{Host: "h.example", ExtraSSH: []string{"-o", "ConnectTimeout=5"}}
	argv := targ.sshArgv("gonf apply -")
	if got := firstConnectTimeout(argv); got != "ConnectTimeout=5" {
		t.Fatalf("argv=%v: first ConnectTimeout=%q, want the explicit 5s", argv, got)
	}
}

// firstOption returns the first "-o Key=Value" option in argv whose Key
// matches prefix (e.g. "ServerAliveInterval="), mirroring
// firstConnectTimeout's "ssh uses the first occurrence" semantics.
func firstOption(argv []string, prefix string) string {
	for _, a := range argv {
		if strings.HasPrefix(a, prefix) {
			return a
		}
	}
	return ""
}

// Every generated ssh argv must set ServerAliveInterval/ServerAliveCountMax:
// ConnectTimeout only bounds the initial handshake, and a per-host/fleet
// timeout only kills the local ssh client's process — it does not by itself
// guarantee prompt detection of a network path that has gone silent without
// tearing down the TCP session. ssh's own keepalive detects and tears down
// that case independently (task x5: "generated ssh has no
// ServerAliveInterval, so a network-level hang ... isn't detected").
func TestSSHArgvServerAliveKeepalive(t *testing.T) {
	argv := PushTarget{Host: "h.example"}.sshArgv("gonf apply -")
	if got := firstOption(argv, "ServerAliveInterval="); got != "ServerAliveInterval=15" {
		t.Fatalf("argv=%v: ServerAliveInterval=%q, want ServerAliveInterval=15", argv, got)
	}
	if got := firstOption(argv, "ServerAliveCountMax="); got != "ServerAliveCountMax=4" {
		t.Fatalf("argv=%v: ServerAliveCountMax=%q, want ServerAliveCountMax=4", argv, got)
	}
}

// An explicit ServerAliveInterval from ExtraSSH (or -- ssh-args) must win
// over the default, exactly like ConnectTimeout: ssh uses the first
// occurrence on the command line, and ExtraSSH comes first.
func TestSSHArgvServerAliveOverride(t *testing.T) {
	targ := PushTarget{Host: "h.example", ExtraSSH: []string{"-o", "ServerAliveInterval=5"}}
	argv := targ.sshArgv("gonf apply -")
	if got := firstOption(argv, "ServerAliveInterval="); got != "ServerAliveInterval=5" {
		t.Fatalf("argv=%v: ServerAliveInterval=%q, want the explicit 5s", argv, got)
	}
}

// TestPushRemoveStickyRunsAfterContextCanceled pins root cause 2: cleanup of
// the remote sticky apply dir must not be skipped just because the push ctx
// that triggered it (SIGINT, -host-timeout, or a sibling host's failure
// aborting the fleet fan-out via errgroup) is already canceled by the time
// pushRemoveSticky runs. Before the fix, pushRemoveSticky ran SSHRunner
// directly on the (possibly canceled) push ctx; exec.CommandContext with an
// already-canceled context never even starts the process, so the real
// SSHRunner would never issue "rm -rf" and the sticky dir would leak.
func TestPushRemoveStickyRunsAfterContextCanceled(t *testing.T) {
	old := SSHRunner
	t.Cleanup(func() { SSHRunner = old })

	var gotArgv []string
	var ctxErrAtCallTime error
	called := false
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		called = true
		// Must be checked here, DURING the call: pushRemoveSticky's own
		// cleanup context is (correctly) canceled by its deferred cancel()
		// once the function returns, so checking it after the call would
		// always see it canceled regardless of whether the fix works.
		ctxErrAtCallTime = ctx.Err()
		gotArgv = append([]string(nil), argv...)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate SIGINT / -host-timeout / a sibling host's Fanout abort

	pushRemoveSticky(ctx, PushTarget{Host: "h.example"}, "/tmp/gonf-apply-sticky-demo")

	if !called {
		t.Fatal("pushRemoveSticky did not invoke SSHRunner at all: cleanup skipped")
	}
	if ctxErrAtCallTime != nil {
		t.Fatalf("cleanup ran with a context that is still canceled/expired (%v); a real "+
			"exec.CommandContext would refuse to start the rm -rf", ctxErrAtCallTime)
	}
	if len(gotArgv) == 0 || !strings.Contains(gotArgv[len(gotArgv)-1], "rm -rf /tmp/gonf-apply-sticky-demo") {
		t.Fatalf("argv=%v, want it to rm -rf the sticky dir", gotArgv)
	}
}

// TestPushToHostRemovesStickyOnBlobUploadFailure covers the pushBlobs-failure
// half of root cause 2: previously a pushBlobs error returned immediately
// with no cleanup attempt at all, leaking any partial upload under the
// sticky dir until the next push happened to reuse (and now wipe) it.
func TestPushToHostRemovesStickyOnBlobUploadFailure(t *testing.T) {
	old := SSHRunner
	restoreProbe := AssumeRemotePlanCurrent()
	t.Cleanup(func() {
		SSHRunner = old
		restoreProbe()
	})

	var removedSticky bool
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		remoteCmd := argv[len(argv)-1]
		if strings.Contains(remoteCmd, "rm -rf") {
			removedSticky = true
			return nil
		}
		// Every non-cleanup call (the blob upload) fails.
		return errors.New("simulated blob upload failure")
	}

	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "demo"},
		{Op: plan.KindFile, Path: "/tmp/unpriv-out", Mode: "0600", ContentB64: "aGVsbG8K"},
		{Op: plan.KindFile, Path: "/tmp/priv-out", Mode: "0600", ContentB64: "aGVsbG8K", Elevate: true},
	}
	mem := plan.NewMemoryStore()
	if _, err := mem.WriteFile("demo.txt", []byte("blob-content")); err != nil {
		t.Fatal(err)
	}

	err := pushToHost(context.Background(),
		PushTarget{Host: "h.example", Privilege: privilege.Sudo}, "demo", ops, mem)
	if err == nil {
		t.Fatal("expected the simulated blob upload failure to propagate")
	}
	if !removedSticky {
		t.Fatal("pushBlobs failure must still attempt to remove the sticky dir")
	}
}

// The real SSHRunner must run its command under the given context: an
// expired context kills the process instead of hanging the push, and the
// error carries the context error so callers can distinguish an aborted push
// from the command's own failure.
func TestSSHRunnerKillsOnContextDeadline(t *testing.T) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := SSHRunner(ctx, nil, []string{"sleep", "5"})
	if err == nil {
		t.Fatal("expected a context-deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("SSHRunner ignored the context deadline: took %v", elapsed)
	}
}
