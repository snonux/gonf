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
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/secret"
)

// setCmdTimeout makes d the process-wide command timeout for one test.
func setCmdTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := gexec.DefaultTimeout()
	t.Cleanup(func() { gexec.SetDefaultTimeout(orig) })
	gexec.SetDefaultTimeout(d)
}

// remoteKind is how the fake remote answers a -cmd-timeout capability probe.
type remoteKind uint8

const (
	// remoteCurrent knows the flag and prints its plan schema.
	remoteCurrent remoteKind = iota
	// remoteOld runs gonf, which rejects the unknown flag the way Go's
	// flag package does (exit 2, the message on stderr).
	remoteOld
	// remoteSudoRefuses never reaches gonf: the sudo/doas rule does not
	// cover the probe command (restricted to "gonf apply *", or a password
	// is required), so nothing can be learned about the binary.
	remoteSudoRefuses
)

// fakeRemoteGonf fakes sshCaptureExec as a remote host for the real
// probeCmdTimeoutSupport, and records every probe command it saw.
type fakeRemoteGonf struct {
	kind   remoteKind
	mu     sync.Mutex
	probes []string
}

func installFakeRemoteGonf(t *testing.T, kind remoteKind) *fakeRemoteGonf {
	t.Helper()
	f := &fakeRemoteGonf{kind: kind}
	oldCapture, oldProber := sshCaptureExec, defaultPusher.CmdTimeoutProber
	t.Cleanup(func() { sshCaptureExec, defaultPusher.CmdTimeoutProber = oldCapture, oldProber })
	defaultPusher.CmdTimeoutProber = probeCmdTimeoutSupport
	sshCaptureExec = func(_ context.Context, argv []string) (string, string, error) {
		cmd := argv[len(argv)-1]
		f.mu.Lock()
		f.probes = append(f.probes, cmd)
		f.mu.Unlock()
		if !strings.Contains(cmd, "-cmd-timeout=") {
			return strconv.Itoa(plan.CurrentVersion) + "\n", "", nil
		}
		switch f.kind {
		case remoteOld:
			return "", unknownCmdTimeoutFlag + "\nUsage of gonf:\n", errors.New("exit status 2")
		case remoteSudoRefuses:
			if strings.HasPrefix(cmd, "sudo ") || strings.HasPrefix(cmd, "doas ") {
				return "", "sudo: a password is required\n", errors.New("exit status 1")
			}
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
// non-default timeout probes exactly the needed contexts. A probe (ssh)
// failure is best-effort (task cc2 finding a): it never surfaces as an
// error, only as an inactive forward for that context, exactly like any
// other unverified verdict.
func TestResolveCmdTimeoutForward(t *testing.T) {
	target := PushTarget{Host: "h.example", Privilege: privilege.Doas}
	var seen []ProbeContext
	p := &Pusher{CmdTimeoutProber: func(_ context.Context, _ PushTarget, pc ProbeContext, flag string) (CmdTimeoutSupport, string, error) {
		if flag != "-cmd-timeout=45s" {
			t.Fatalf("probe flag = %q", flag)
		}
		seen = append(seen, pc)
		if pc == ProbeLogin {
			return CmdTimeoutAccepted, "", nil
		}
		return CmdTimeoutUnknownFlag, "", nil
	}}

	setCmdTimeout(t, gexec.BuiltinDefaultTimeout)
	if f := p.resolveCmdTimeoutForward(context.Background(), target, true, true); f.active() || len(seen) != 0 {
		t.Fatalf("default timeout: fwd=%+v probes=%v, want nothing", f, seen)
	}

	setCmdTimeout(t, 45*time.Second)
	f := p.resolveCmdTimeoutForward(context.Background(), target, true, true)
	if !f.login || f.elevated || len(seen) != 2 {
		t.Fatalf("fwd=%+v probes=%v, want login only after two probes", f, seen)
	}
	seen = nil
	if f := p.resolveCmdTimeoutForward(context.Background(), target, false, true); f.active() || len(seen) != 1 || seen[0] != ProbeElevated {
		t.Fatalf("elevated-only: fwd=%+v probes=%v, want one elevated probe and nothing forwarded", f, seen)
	}

	// A probe ssh failure (the prober itself erroring, as opposed to
	// answering CmdTimeoutUnverified) must not propagate: it is folded into
	// "unverified" so the caller (prepareRemote, payloadApplyCmd) never
	// fails the push/payload over it.
	boom := errors.New("ssh boom")
	p.CmdTimeoutProber = func(context.Context, PushTarget, ProbeContext, string) (CmdTimeoutSupport, string, error) {
		return CmdTimeoutUnverified, "", boom
	}
	if f := p.resolveCmdTimeoutForward(context.Background(), target, true, false); f.active() {
		t.Fatalf("probe ssh failure: fwd=%+v, want nothing forwarded (not an error)", f)
	}
	if f := (&Pusher{}).resolveCmdTimeoutForward(context.Background(), target, true, true); f.active() {
		t.Fatalf("nil prober: fwd=%+v, want nothing forwarded", f)
	}
}

// The real probe asks the binary itself, in the chunk's privilege context,
// and classifies the answer: accepted, too old, or (the case a plain
// "supported / not supported" bool got wrong) not answered at all because
// sudo/doas refused the probe command.
func TestProbeCmdTimeoutSupport(t *testing.T) {
	tests := []struct {
		name       string
		kind       remoteKind
		want       CmdTimeoutSupport
		wantDetail string
	}{
		{"current", remoteCurrent, CmdTimeoutAccepted, ""},
		{"old gonf", remoteOld, CmdTimeoutUnknownFlag, ""},
		{"sudo refuses", remoteSudoRefuses, CmdTimeoutUnverified, "sudo: a password is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := installFakeRemoteGonf(t, tc.kind)
			target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
			got, detail, err := probeCmdTimeoutSupport(context.Background(), target, ProbeElevated, "-cmd-timeout=30s")
			if err != nil || got != tc.want || detail != tc.wantDetail {
				t.Fatalf("verdict=%v detail=%q err=%v, want %v %q", got, detail, err, tc.want, tc.wantDetail)
			}
			if got := f.probeCmds(); len(got) != 1 || got[0] != "sudo -n gonf -cmd-timeout=30s -plan-version" {
				t.Fatalf("probe cmds = %v", got)
			}
		})
	}
}

// classifyCmdTimeoutProbe reads the streams, not the exit status: a plan
// schema means accepted, the flag package's message means an old gonf, and
// everything else is unverified with a one-line reason for the warning.
func TestClassifyCmdTimeoutProbe(t *testing.T) {
	tests := []struct {
		name       string
		stdout     string
		stderr     string
		want       CmdTimeoutSupport
		wantDetail string
	}{
		{"schema", "3\n", "", CmdTimeoutAccepted, ""},
		{"unknown flag", "", unknownCmdTimeoutFlag + "\nUsage of gonf:\n", CmdTimeoutUnknownFlag, ""},
		{"doas refusal", "", "\ndoas: Operation not permitted\n", CmdTimeoutUnverified, "doas: Operation not permitted"},
		{"missing binary", "", "sh: gonf: not found\n", CmdTimeoutUnverified, "sh: gonf: not found"},
		{"banner noise", "Welcome to host\n", "", CmdTimeoutUnverified, `unexpected output "Welcome to host"`},
		{"silence", "", "", CmdTimeoutUnverified, "no output"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := classifyCmdTimeoutProbe(tc.stdout, tc.stderr)
			if got != tc.want || detail != tc.wantDetail {
				t.Fatalf("verdict=%v detail=%q, want %v %q", got, detail, tc.want, tc.wantDetail)
			}
		})
	}
}

// The non-forwarding verdicts must be worded apart, and (task cc2 finding
// b) an old-gonf verdict must itself be worded apart by privilege context:
// a login probe is fixed by the very push that ran it, but an elevated
// probe answers for whatever gonf sudo/doas's secure_path resolves, a
// binary EnsureRemoteGonf never installs or upgrades, so telling the
// operator to "push" there would point at a step that cannot fix it.
func TestCmdTimeoutWarningWording(t *testing.T) {
	tests := []struct {
		name                    string
		kind                    remoteKind
		needLogin, needElevated bool
		want, notWant           string
	}{
		{"old gonf, login context", remoteOld, true, false,
			"too old for -cmd-timeout=30s; its apply runs with the remote default command timeout (push to upgrade gonf there)",
			"sudo/doas resolves"},
		{"old gonf, elevated context", remoteOld, false, true,
			`the gonf sudo/doas resolves at "gonf" is older than the one push installs`,
			"push to upgrade gonf there"},
		{"sudo refuses", remoteSudoRefuses, false, true,
			"could not run gonf in the elevated (sudo/doas) context", "too old"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			installFakeRemoteGonf(t, tc.kind)
			setCmdTimeout(t, 30*time.Second)
			out := testutil.CaptureLog(t, logger.LevelWarn)
			target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
			fwd := defaultPusher.resolveCmdTimeoutForward(context.Background(), target, tc.needLogin, tc.needElevated)
			if fwd.active() {
				t.Fatalf("fwd=%+v, want nothing forwarded", fwd)
			}
			if got := out(); !strings.Contains(got, tc.want) || strings.Contains(got, tc.notWant) {
				t.Fatalf("warning = %q, want it to contain %q and not %q", got, tc.want, tc.notWant)
			}
		})
	}
}

// installFakeSecret tracks secretValue with a real secret.Values (the same
// type api installs with logger.SetRedactor in production) for one test,
// and removes it again on cleanup.
func installFakeSecret(t *testing.T, secretValue string) {
	t.Helper()
	var values secret.Values
	values.Add([]byte(secretValue))
	logger.SetRedactor(&values)
	t.Cleanup(func() { logger.SetRedactor(nil) })
}

// firstLine must redact before it truncates: cutting the raw text at 200
// bytes first can slice a secret in half, and secret.Values only recognises
// a secret's complete bytes (see secret/values.go), so the surviving half
// would then pass Redact unrecognised and leak. A secret planted so it
// starts before byte 200 and ends after it catches a regression to
// truncate-then-redact.
func TestFirstLineRedactsBeforeTruncating(t *testing.T) {
	secretValue := "S3cr3t_" + strings.Repeat("Q", 33) // 40 bytes
	installFakeSecret(t, secretValue)

	// Chosen so the secret straddles the 200-byte cut (prefix < 200 <
	// prefix+len(secretValue)) while still leaving room for the much
	// shorter secret.Redacted marker to survive that same cut once the
	// secret is replaced by it (prefix+len(secret.Redacted) < 200).
	prefix := strings.Repeat("a", 170)
	if boundary := len(prefix); boundary >= 200 || boundary+len(secretValue) <= 200 {
		t.Fatalf("test setup: secret (len %d) starting at %d does not straddle the 200-byte cut", len(secretValue), boundary)
	}
	got := firstLine(prefix + secretValue + " tail")

	if !strings.Contains(got, secret.Redacted) {
		t.Fatalf("firstLine did not redact at all: %q", got)
	}
	// A truncate-then-redact regression would keep exactly this fragment (the
	// secret's bytes up to the 200-byte cut) untouched, since Redact only
	// recognises a tracked value's complete bytes, never a partial one.
	if leak := secretValue[:200-len(prefix)]; strings.Contains(got, leak) {
		t.Fatalf("firstLine leaked the pre-cut fragment %q: %q", leak, got)
	}
	// No shorter fragment (secret.MinContainedLen or longer) may survive
	// either.
	for n := len(secretValue); n >= secret.MinContainedLen; n-- {
		if strings.Contains(got, secretValue[:n]) {
			t.Fatalf("firstLine leaked a %d-byte fragment of the secret: %q", n, got)
		}
	}
}

// A secret in a probe's stderr must not reach the logged warning either:
// classifyCmdTimeoutProbe's detail is built from firstLine, and
// acceptsCmdTimeout interpolates it straight into the warning it logs. The
// secret again straddles firstLine's 200-byte cut.
func TestCmdTimeoutWarningRedactsProbeStderr(t *testing.T) {
	secretValue := "S3cr3t_" + strings.Repeat("Q", 33) // 40 bytes
	installFakeSecret(t, secretValue)

	oldCapture, oldProber := sshCaptureExec, defaultPusher.CmdTimeoutProber
	t.Cleanup(func() { sshCaptureExec, defaultPusher.CmdTimeoutProber = oldCapture, oldProber })
	defaultPusher.CmdTimeoutProber = probeCmdTimeoutSupport
	sshCaptureExec = func(_ context.Context, argv []string) (string, string, error) {
		cmd := argv[len(argv)-1]
		if !strings.Contains(cmd, "-cmd-timeout=") {
			return strconv.Itoa(plan.CurrentVersion) + "\n", "", nil
		}
		// "doas: " (6 bytes) + 164 filler bytes = a 170-byte prefix, chosen
		// like TestFirstLineRedactsBeforeTruncating's so the secret
		// straddles firstLine's 200-byte cut while the much shorter
		// secret.Redacted marker that replaces it still fits before the cut.
		return "", "doas: " + strings.Repeat("a", 164) + secretValue + " Operation not permitted\n", errors.New("exit status 1")
	}

	setCmdTimeout(t, 30*time.Second)
	out := testutil.CaptureLog(t, logger.LevelWarn)
	target := PushTarget{Host: "h.example", Privilege: privilege.Doas}
	fwd := defaultPusher.resolveCmdTimeoutForward(context.Background(), target, false, true)
	if fwd.active() {
		t.Fatalf("fwd=%+v, want nothing forwarded", fwd)
	}
	warning := out()
	if !strings.Contains(warning, secret.Redacted) {
		t.Fatalf("warning was not redacted at all: %q", warning)
	}
	for n := len(secretValue); n >= secret.MinContainedLen; n-- {
		if strings.Contains(warning, secretValue[:n]) {
			t.Fatalf("warning leaked a %d-byte fragment of the probe's secret: %q", n, warning)
		}
	}
}

// End to end through Delivery.ToHost with a fake ssh: a non-default
// -cmd-timeout reaches every chunk's apply (login and elevated) on a remote
// gonf that knows the flag, and is left out, without failing the push, on
// an older remote gonf that would reject it (version skew).
func TestToHostForwardsCmdTimeoutOnlyToCapableRemote(t *testing.T) {
	tests := []struct {
		name string
		kind remoteKind
		want []string
	}{
		{"capable remote", remoteCurrent, []string{"gonf -cmd-timeout=30s apply -", "sudo -n gonf -cmd-timeout=30s apply -"}},
		{"old remote", remoteOld, []string{"gonf apply -", "sudo -n gonf apply -"}},
		{"sudo refuses the probe", remoteSudoRefuses, []string{"gonf -cmd-timeout=30s apply -", "sudo -n gonf apply -"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := installDeliveryRecorder(t)
			f := installFakeRemoteGonf(t, tc.kind)
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

// Preview shares prepareRemote with Push (see delivery.go), so the same
// capability gating applies to a preview's "-n -strict-preview -" apply
// argument, and never upgrades a remote gonf too old for the flag.
func TestPreviewToHostForwardsCmdTimeoutOnlyToCapableRemote(t *testing.T) {
	tests := []struct {
		name string
		kind remoteKind
		want []string
	}{
		{"capable remote", remoteCurrent, []string{
			"gonf -cmd-timeout=30s apply -n -strict-preview -",
			"sudo -n gonf -cmd-timeout=30s apply -n -strict-preview -",
		}},
		{"old remote", remoteOld, []string{
			"gonf apply -n -strict-preview -",
			"sudo -n gonf apply -n -strict-preview -",
		}},
		{"sudo refuses the probe", remoteSudoRefuses, []string{
			"gonf -cmd-timeout=30s apply -n -strict-preview -",
			"sudo -n gonf apply -n -strict-preview -",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := installDeliveryRecorder(t)
			f := installFakeRemoteGonf(t, tc.kind)
			setCmdTimeout(t, 30*time.Second)
			ops := []plan.Op{
				{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "p"},
				{Op: plan.KindFile, Path: "/tmp/unpriv-out", Mode: "0600", ContentB64: "aGVsbG8K"},
				{Op: plan.KindFile, Path: "/tmp/priv-out", Mode: "0600", ContentB64: "aGVsbG8K", Elevate: true},
			}
			target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
			if err := previewToHost(context.Background(), target, "p", ops, nil); err != nil {
				t.Fatalf("preview: %v", err)
			}
			if got := r.cmds(); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("remote apply cmds = %v, want %v", got, tc.want)
			}
			if got := f.probeCmds(); len(got) != 2 {
				t.Fatalf("capability probes = %v, want one per privilege context", got)
			}
			if r.bootstraps.Load() != 0 {
				t.Fatalf("preview bootstrapped/installed gonf: %d", r.bootstraps.Load())
			}
		})
	}
}

// At the built-in default a push sends the unchanged argv and opens no
// capability probe at all.
func TestToHostDefaultCmdTimeoutNotForwarded(t *testing.T) {
	r := installDeliveryRecorder(t)
	f := installFakeRemoteGonf(t, remoteCurrent)
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
	for _, kind := range []remoteKind{remoteCurrent, remoteOld, remoteSudoRefuses} {
		r := installDeliveryRecorder(t)
		installFakeRemoteGonf(t, kind)
		setCmdTimeout(t, 30*time.Second)
		target := PushTarget{Host: "h.example", Privilege: privilege.Doas}
		if err := PushPayloadContext(context.Background(), target, []byte("GONF-PUSH/1"), true, ""); err != nil {
			t.Fatalf("kind=%v: push: %v", kind, err)
		}
		want := "doas gonf apply -"
		if kind == remoteCurrent {
			want = "doas gonf -cmd-timeout=30s apply -"
		}
		if got := r.cmds(); len(got) != 1 || got[0] != want {
			t.Fatalf("kind=%v: remote cmds = %v, want %q", kind, got, want)
		}
	}
}

// Task cc2 finding (a): a probe's own ssh round trip can fail (a transient
// network blip, a host that dropped the connection) independently of what
// the remote gonf would have answered. That must not fail the whole
// push/PushPayload — forwarding -cmd-timeout is a nicety, not a hard
// requirement — it must instead warn and fall back to the remote's default
// command timeout, exactly like any other unverified verdict (contrast with
// remoteReleaseIsStale elsewhere in this package, which has always been
// best-effort this way). Both delivery.go's prepareRemote (via a Push) and
// remote.go's payloadApplyCmd (via PushPayloadContext) go through
// resolveCmdTimeoutForward, so both are exercised here.
func TestCmdTimeoutProbeSSHFailureIsBestEffort(t *testing.T) {
	r := installDeliveryRecorder(t)
	setCmdTimeout(t, 30*time.Second)
	boom := errors.New("ssh: connect to host h.example port 22: connection refused")
	oldProber := defaultPusher.CmdTimeoutProber
	defaultPusher.CmdTimeoutProber = func(context.Context, PushTarget, ProbeContext, string) (CmdTimeoutSupport, string, error) {
		return CmdTimeoutUnverified, "", boom
	}
	t.Cleanup(func() { defaultPusher.CmdTimeoutProber = oldProber })

	out := testutil.CaptureLog(t, logger.LevelWarn)
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "p"},
		{Op: plan.KindFile, Path: "/tmp/unpriv-out", Mode: "0600", ContentB64: "aGVsbG8K"},
		{Op: plan.KindFile, Path: "/tmp/priv-out", Mode: "0600", ContentB64: "aGVsbG8K", Elevate: true},
	}
	target := PushTarget{Host: "h.example", Privilege: privilege.Sudo}
	if err := pushToHost(context.Background(), target, "p", ops, nil); err != nil {
		t.Fatalf("push failed on a probe ssh failure, want best-effort (warn, omit, continue): %v", err)
	}
	want := []string{"gonf apply -", "sudo -n gonf apply -"}
	if got := r.cmds(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("remote apply cmds = %v, want -cmd-timeout left out of both: %v", got, want)
	}
	if got := out(); !strings.Contains(got, boom.Error()) {
		t.Fatalf("warning = %q, want it to mention the probe failure %q", got, boom.Error())
	}

	// PushPayloadContext is the other caller of resolveCmdTimeoutForward
	// (it never installs gonf, so it is not covered by the push above).
	if err := PushPayloadContext(context.Background(), target, []byte("GONF-PUSH/1"), true, ""); err != nil {
		t.Fatalf("PushPayloadContext failed on a probe ssh failure, want best-effort: %v", err)
	}
	if got := r.cmds(); got[len(got)-1] != "sudo -n gonf apply -" {
		t.Fatalf("PushPayloadContext remote cmd = %q, want -cmd-timeout left out", got[len(got)-1])
	}
}
