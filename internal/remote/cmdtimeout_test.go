package remote

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// setCmdTimeout makes d the process-wide command timeout for one test.
func setCmdTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := gexec.DefaultTimeout()
	t.Cleanup(func() { gexec.SetDefaultTimeout(orig) })
	gexec.SetDefaultTimeout(d)
}

// fakeRemoteGonf fakes sshCaptureExec as a remote gonf binary for the real
// probeCmdTimeoutSupport: a binary without the flag (knowsFlag false)
// rejects "-cmd-timeout=..." the way flag parsing does (exit 2, nothing on
// stdout); otherwise it prints its plan schema. It records every probe.
type fakeRemoteGonf struct {
	knowsFlag bool
	mu        sync.Mutex
	probes    []string
}

func installFakeRemoteGonf(t *testing.T, knowsFlag bool) *fakeRemoteGonf {
	t.Helper()
	f := &fakeRemoteGonf{knowsFlag: knowsFlag}
	oldCapture, oldProber := sshCaptureExec, defaultPusher.CmdTimeoutProber
	t.Cleanup(func() { sshCaptureExec, defaultPusher.CmdTimeoutProber = oldCapture, oldProber })
	defaultPusher.CmdTimeoutProber = probeCmdTimeoutSupport
	sshCaptureExec = func(_ context.Context, argv []string) (string, string, error) {
		cmd := argv[len(argv)-1]
		f.mu.Lock()
		f.probes = append(f.probes, cmd)
		f.mu.Unlock()
		if strings.Contains(cmd, "-cmd-timeout=") && !f.knowsFlag {
			return "", "flag provided but not defined: -cmd-timeout", errors.New("exit status 2")
		}
		return strconv.Itoa(plan.CurrentVersion) + "\n", "", nil
	}
	return f
}

func (f *fakeRemoteGonf) probeCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.probes...)
}

// remoteApplyCmd places the forwarded flag before "apply", and only for the
// privilege context whose binary accepted it.
func TestRemoteApplyCmdCmdTimeoutForward(t *testing.T) {
	both := cmdTimeoutForward{flag: "-cmd-timeout=30s", login: true, elevated: true}
	loginOnly := cmdTimeoutForward{flag: "-cmd-timeout=30s", login: true}
	tests := []struct {
		name    string
		elevate bool
		dir     string
		fwd     cmdTimeoutForward
		want    string
	}{
		{"none", false, "", cmdTimeoutForward{}, "gonf apply -"},
		{"login", false, "", both, "gonf -cmd-timeout=30s apply -"},
		{"elevated", true, "/tmp/s", both, "sudo -n gonf -cmd-timeout=30s apply -apply-dir /tmp/s -"},
		{"elevated not accepted", true, "", loginOnly, "sudo -n gonf apply -"},
		{"login not accepted", false, "", cmdTimeoutForward{flag: "-cmd-timeout=30s", elevated: true}, "gonf apply -"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remoteApplyCmd(tc.elevate, PushTarget{Privilege: privilege.Sudo}, tc.dir, Push, tc.fwd)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// At the built-in default no probe runs and nothing is forwarded; a
// non-default timeout probes exactly the needed contexts, and a probe (ssh)
// failure fails the resolution.
func TestResolveCmdTimeoutForward(t *testing.T) {
	target := PushTarget{Host: "h.example", Privilege: privilege.Doas}
	var seen []ProbeContext
	p := &Pusher{CmdTimeoutProber: func(_ context.Context, _ PushTarget, pc ProbeContext, flag string) (bool, error) {
		if flag != "-cmd-timeout=45s" {
			t.Fatalf("probe flag = %q", flag)
		}
		seen = append(seen, pc)
		return pc == ProbeLogin, nil
	}}

	setCmdTimeout(t, gexec.BuiltinDefaultTimeout)
	if f, err := p.resolveCmdTimeoutForward(context.Background(), target, true, true); err != nil || f.active() || len(seen) != 0 {
		t.Fatalf("default timeout: fwd=%+v err=%v probes=%v, want nothing", f, err, seen)
	}

	setCmdTimeout(t, 45*time.Second)
	f, err := p.resolveCmdTimeoutForward(context.Background(), target, true, true)
	if err != nil || !f.login || f.elevated || len(seen) != 2 {
		t.Fatalf("fwd=%+v err=%v probes=%v, want login only after two probes", f, err, seen)
	}
	seen = nil
	if _, err := p.resolveCmdTimeoutForward(context.Background(), target, false, true); err != nil || len(seen) != 1 || seen[0] != ProbeElevated {
		t.Fatalf("elevated-only: err=%v probes=%v, want one elevated probe", err, seen)
	}

	boom := errors.New("ssh boom")
	p.CmdTimeoutProber = func(context.Context, PushTarget, ProbeContext, string) (bool, error) { return false, boom }
	if _, err := p.resolveCmdTimeoutForward(context.Background(), target, true, false); !errors.Is(err, boom) {
		t.Fatalf("probe failure = %v, want %v", err, boom)
	}
	if f, err := (&Pusher{}).resolveCmdTimeoutForward(context.Background(), target, true, true); err != nil || f.active() {
		t.Fatalf("nil prober: fwd=%+v err=%v, want nothing forwarded", f, err)
	}
}

// The real probe asks the binary itself, in the chunk's privilege context,
// and treats a flag-parse failure as "not accepted" rather than an error.
func TestProbeCmdTimeoutSupport(t *testing.T) {
	for _, knows := range []bool{true, false} {
		f := installFakeRemoteGonf(t, knows)
		target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
		ok, err := probeCmdTimeoutSupport(context.Background(), target, ProbeElevated, "-cmd-timeout=30s")
		if err != nil || ok != knows {
			t.Fatalf("knowsFlag=%v: ok=%v err=%v", knows, ok, err)
		}
		if got := f.probeCmds(); len(got) != 1 || got[0] != "sudo -n gonf -cmd-timeout=30s -plan-version" {
			t.Fatalf("probe cmds = %v", got)
		}
	}
}

// End to end through Delivery.ToHost with a fake ssh: a non-default
// -cmd-timeout reaches every chunk's apply (login and elevated) on a remote
// gonf that knows the flag, and is left out, without failing the push, on
// an older remote gonf that would reject it (version skew).
func TestToHostForwardsCmdTimeoutOnlyToCapableRemote(t *testing.T) {
	tests := []struct {
		name      string
		knowsFlag bool
		want      []string
	}{
		{"capable remote", true, []string{"gonf -cmd-timeout=30s apply -", "sudo -n gonf -cmd-timeout=30s apply -"}},
		{"old remote", false, []string{"gonf apply -", "sudo -n gonf apply -"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := installDeliveryRecorder(t)
			f := installFakeRemoteGonf(t, tc.knowsFlag)
			setCmdTimeout(t, 30*time.Second)
			ops := []plan.Op{
				{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "p"},
				{Op: plan.KindFile, Path: "/tmp/unpriv-out", Mode: "0600", ContentB64: "aGVsbG8K"},
				{Op: plan.KindFile, Path: "/tmp/priv-out", Mode: "0600", ContentB64: "aGVsbG8K", Elevate: true},
			}
			target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
			if err := pushToHost(context.Background(), target, "p", ops, nil); err != nil {
				t.Fatalf("push: %v", err)
			}
			if got := r.cmds(); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("remote apply cmds = %v, want %v", got, tc.want)
			}
			if got := f.probeCmds(); len(got) != 2 {
				t.Fatalf("capability probes = %v, want one per privilege context", got)
			}
		})
	}
}

// At the built-in default a push sends the unchanged argv and opens no
// capability probe at all.
func TestToHostDefaultCmdTimeoutNotForwarded(t *testing.T) {
	r := installDeliveryRecorder(t)
	f := installFakeRemoteGonf(t, true)
	setCmdTimeout(t, gexec.BuiltinDefaultTimeout)
	if err := pushToHost(context.Background(), PushTarget{Host: "h.example"}, "demo", deliveryOps(), nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := r.cmds(); len(got) != 1 || got[0] != "gonf apply -" {
		t.Fatalf("remote cmds = %v, want the plain apply", got)
	}
	if got := f.probeCmds(); len(got) != 0 {
		t.Fatalf("capability probes at the default timeout = %v, want none", got)
	}
}

// PushPayloadContext (the raw-payload path, which never upgrades gonf) is
// gated the same way.
func TestPushPayloadForwardsCmdTimeoutOnlyToCapableRemote(t *testing.T) {
	for _, knows := range []bool{true, false} {
		r := installDeliveryRecorder(t)
		installFakeRemoteGonf(t, knows)
		setCmdTimeout(t, 30*time.Second)
		target := PushTarget{Host: "h.example", Privilege: privilege.Doas}
		if err := PushPayloadContext(context.Background(), target, []byte("GONF-PUSH/1"), true, ""); err != nil {
			t.Fatalf("knowsFlag=%v: push: %v", knows, err)
		}
		want := "doas gonf apply -"
		if knows {
			want = "doas gonf -cmd-timeout=30s apply -"
		}
		if got := r.cmds(); len(got) != 1 || got[0] != want {
			t.Fatalf("knowsFlag=%v: remote cmds = %v, want %q", knows, got, want)
		}
	}
}
