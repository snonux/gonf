package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/internal/privilege"
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
			got, err := remoteApplyCmd(tc.elevate, PushTarget{Privilege: tc.mode}, "")
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
