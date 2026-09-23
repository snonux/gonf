package remote

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	gexec "github.com/snonux/gonf/internal/exec"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/plan"
)

// Forwarding the controller's -cmd-timeout to the remote gonf apply (task
// c82). Without it, a remote chunk's backend commands and File/ConfigSet
// validators run under the remote binary's built-in default
// (gexec.BuiltinDefaultTimeout) whatever the controller was told.
//
// Version skew: -cmd-timeout is a global flag of newer gonf binaries only.
// A remote binary that does not know it rejects the whole command line
// ("flag provided but not defined", exit 2), so passing it blindly would
// break every push to such a host. The release/plan-schema checks cannot
// gate it: the flag landed without a plan-schema bump and within a release
// (0.15.0 binaries exist with and without it), and a push does not always
// upgrade (PushPayload never does; preview only verifies). So the flag is
// gated on a direct capability probe of the binary that will run the chunk,
// in its privilege context (sudo/doas can resolve a different gonf than the
// login PATH): "gonf -cmd-timeout=<d> -plan-version" prints the plan schema
// only when the binary parses that flag and value.
//
// Cost: the probe runs only when the controller's timeout differs from the
// built-in default (the common case sends the unchanged argv and probes
// nothing). Then it adds one ssh round trip per host and privilege context
// a chunk applies in, and for elevated chunks one extra "sudo -n gonf
// -cmd-timeout=<d> -plan-version" (or doas) invocation besides the apply
// itself. A sudoers/doas rule restricted to "gonf apply *", or one that
// needs a password, refuses that probe; the probe then cannot tell anything
// about the binary.
//
// Any verdict other than "accepted" — including the probe's own ssh round
// trip failing — leaves the flag out, so the apply runs under the remote's
// own default command timeout, and never fails the push or PushPayload:
// forwarding -cmd-timeout is a nicety, not a hard requirement, so a probe
// failure only warns and is folded into "unverified" like any other
// inconclusive verdict (the same best-effort contract remoteReleaseIsStale
// already keeps elsewhere in this package). The warning says why: a binary
// too old for the flag (fixed by upgrading the gonf that resolves in that
// privilege context) differs from a probe that could not run gonf at all
// (sudo/doas refused it, gonf missing, the ssh round trip itself failed).

// CmdTimeoutSupport is the verdict of a -cmd-timeout capability probe of one
// remote gonf binary in one privilege context. The zero value is
// CmdTimeoutUnverified, so an unset verdict never forwards the flag.
type CmdTimeoutSupport uint8

const (
	// CmdTimeoutUnverified means gonf could not be run to check: the
	// sudo/doas wrapper refused the probe, the binary is missing, or its
	// output was not a plan schema (e.g. login banner noise).
	CmdTimeoutUnverified CmdTimeoutSupport = iota
	// CmdTimeoutAccepted means the binary parsed the flag and value.
	CmdTimeoutAccepted
	// CmdTimeoutUnknownFlag means the binary ran and rejected the flag as
	// undefined: it predates -cmd-timeout.
	CmdTimeoutUnknownFlag
)

// unknownCmdTimeoutFlag is what Go's flag package prints (on stderr, with
// exit 2) when a gonf binary that predates -cmd-timeout is handed it.
const unknownCmdTimeoutFlag = "flag provided but not defined: -cmd-timeout"

// cmdTimeoutForward is the resolved -cmd-timeout forwarding for one host:
// the global flag to pass ("" for none) and, per privilege context, whether
// that context's gonf binary accepts it. The zero value forwards nothing.
type cmdTimeoutForward struct {
	flag     string
	login    bool
	elevated bool
}

// active reports whether the flag is forwarded to any privilege context.
func (f cmdTimeoutForward) active() bool {
	return f.flag != "" && (f.login || f.elevated)
}

// prefix returns the "-cmd-timeout=<d> " argument prefix for a session in
// the given privilege context, or "" when the flag is not forwarded there.
func (f cmdTimeoutForward) prefix(elevate bool) string {
	accepted := f.login
	if elevate {
		accepted = f.elevated
	}
	if f.flag == "" || !accepted {
		return ""
	}
	return f.flag + " "
}

// chunkContexts reports which privilege contexts the chunks apply in: the
// SSH login (unelevated chunks) and/or sudo/doas (elevated chunks).
func chunkContexts(chunks []plan.Chunk) (needLogin, needElevated bool) {
	for _, chunk := range chunks {
		if chunk.Elevate {
			needElevated = true
		} else {
			needLogin = true
		}
	}
	return needLogin, needElevated
}

// resolveCmdTimeoutForward decides the -cmd-timeout forwarding for t from
// the controller's current command timeout (gexec.DefaultTimeout, set by
// the CLI's -cmd-timeout or api.SetCommandTimeout). At the built-in default
// it returns the zero value without any probe. Otherwise it probes each
// needed privilege context (see the file comment): every verdict, including
// a probe ssh failure, only ever warns and forwards nothing for that
// context, so this never fails the push/payload — there is no error to
// return.
func (p *Pusher) resolveCmdTimeoutForward(ctx context.Context, t PushTarget, needLogin, needElevated bool) cmdTimeoutForward {
	f := cmdTimeoutForward{flag: gexec.CmdTimeoutFlag(gexec.DefaultTimeout())}
	if f.flag == "" {
		return f
	}
	if needLogin {
		f.login = p.acceptsCmdTimeout(ctx, t, ProbeLogin, f.flag)
	}
	if needElevated {
		f.elevated = p.acceptsCmdTimeout(ctx, t, ProbeElevated, f.flag)
	}
	return f
}

// acceptsCmdTimeout runs p.CmdTimeoutProber for one privilege context and
// warns, worded by verdict, when the flag will not be forwarded there. A
// nil prober (a hand-built Pusher) cannot verify support, so nothing is
// forwarded. A prober error (the probe's ssh round trip itself failing, as
// opposed to the remote command merely failing) is folded into the
// CmdTimeoutUnverified case rather than returned: -cmd-timeout forwarding is
// a nicety, so a transient ssh failure here must warn and continue rather
// than fail the whole push/payload (the caller has no error branch left to
// handle it).
func (p *Pusher) acceptsCmdTimeout(ctx context.Context, t PushTarget, pc ProbeContext, flag string) bool {
	verdict, detail := CmdTimeoutUnverified, "no capability prober"
	if p.CmdTimeoutProber != nil {
		var err error
		if verdict, detail, err = p.CmdTimeoutProber(ctx, t, pc, flag); err != nil {
			verdict, detail = CmdTimeoutUnverified, firstLine(err.Error())
		}
	}
	switch verdict {
	case CmdTimeoutAccepted:
		return true
	case CmdTimeoutUnknownFlag:
		warnCmdTimeoutTooOld(t, pc, flag)
	default:
		logger.Warn("push %s: could not run gonf in the %s context to check %s support (%s); its apply runs "+
			"without it, with the remote default command timeout", t.Destination(), probeContextLabel(pc), flag, detail)
	}
	return false
}

// warnCmdTimeoutTooOld logs the CmdTimeoutUnknownFlag warning, worded by
// privilege context: a login probe was fixed by the very push that just
// ran (push upgrades the login binary), but an elevated probe answers for
// whatever gonf sudo/doas's secure_path resolves — a path EnsureRemoteGonf
// never installs or upgrades — so telling the operator to "push" there
// would send them to repeat a step that cannot touch that binary at all.
func warnCmdTimeoutTooOld(t PushTarget, pc ProbeContext, flag string) {
	if pc == ProbeElevated {
		logger.Warn("push %s: the gonf sudo/doas resolves at %q is older than the one push installs; its apply runs "+
			"with the remote default command timeout (upgrade or align it, e.g. sudo's secure_path, so it resolves the same gonf push installs)",
			t.Destination(), remoteGonfBin(t))
		return
	}
	logger.Warn("push %s: the %s gonf is too old for %s; its apply runs with the remote default command timeout "+
		"(push to upgrade gonf there)", t.Destination(), probeContextLabel(pc), flag)
}

// probeContextLabel names pc in a warning: the SSH login's gonf or the one
// sudo/doas runs.
func probeContextLabel(pc ProbeContext) string {
	if pc == ProbeElevated {
		return "elevated (sudo/doas)"
	}
	return "SSH login"
}

// probeCmdTimeoutSupport is the production CmdTimeoutProber: it runs
// "gonf <flag> -plan-version" in pc and classifies the result
// (classifyCmdTimeoutProbe). Only an ssh failure (or the push context
// ending) is an error; acceptsCmdTimeout treats even that as an unverified
// verdict rather than a push-ending failure (see its doc comment).
func probeCmdTimeoutSupport(ctx context.Context, t PushTarget, pc ProbeContext, flag string) (CmdTimeoutSupport, string, error) {
	cmd, err := remoteProbeCmd(t, pc, flag+" -plan-version")
	if err != nil {
		return CmdTimeoutUnverified, "", err
	}
	stdout, stderr, err := sshCaptureWithStderr(ctx, t, cmd)
	if err != nil {
		return CmdTimeoutUnverified, "", err
	}
	verdict, detail := classifyCmdTimeoutProbe(stdout, stderr)
	return verdict, detail, nil
}

// classifyCmdTimeoutProbe turns a probe's output into a verdict and, for
// CmdTimeoutUnverified, a one-line reason for the warning. A plan schema
// integer on stdout means the flag was accepted. Go's "flag provided but
// not defined: -cmd-timeout" on stderr means gonf itself ran and is too
// old. Anything else did not reach a gonf that could answer: the sudo/doas
// refusal ("a password is required", "not allowed to execute"), a missing
// binary, or unparseable stdout (a login banner mixed into it).
func classifyCmdTimeoutProbe(stdout, stderr string) (CmdTimeoutSupport, string) {
	out := strings.TrimSpace(stdout)
	if _, err := strconv.Atoi(out); err == nil {
		return CmdTimeoutAccepted, ""
	}
	if strings.Contains(stderr, unknownCmdTimeoutFlag) {
		return CmdTimeoutUnknownFlag, ""
	}
	if line := firstLine(stderr); line != "" {
		return CmdTimeoutUnverified, line
	}
	if out != "" {
		return CmdTimeoutUnverified, fmt.Sprintf("unexpected output %q", firstLine(out))
	}
	return CmdTimeoutUnverified, "no output"
}

// firstLine returns s's first non-empty line, trimmed, redacted
// (logger.Redact) and then capped at 200 bytes (cut on a rune boundary, so
// multi-byte UTF-8 text is never split) so a long error cannot flood the
// warning. Its result feeds straight into the "could not run gonf" warning
// (see acceptsCmdTimeout), and s is raw remote stderr/stdout that gonf never
// controls (a sudoers/PAM refusal, an SSH banner) and that the controller's
// secret registry never saw, so it must go through gonf's redaction
// contract like any other quoted remote output.
//
// Redact runs before the cut, not after: truncating first could slice a
// secret in half, and secret.Values only recognises a tracked value's
// complete bytes, so the surviving half would then pass through Redact
// unrecognised. Redacting the untruncated line first guarantees a whole
// secret present in it is replaced before any cut can split it; the cut
// then simply bounds whatever (possibly shorter, once redacted) text
// remains, exactly as before.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			line = logger.Redact(line)
			if len(line) > 200 {
				cut := 200
				for cut > 0 && !utf8.RuneStart(line[cut]) {
					cut--
				}
				line = line[:cut] + "..."
			}
			return line
		}
	}
	return ""
}

// acceptCmdTimeout is the fake CmdTimeoutProber installed by the test seams
// that model a current remote gonf (Assume*, ObserveBootstrapForTest): it
// accepts the flag.
func acceptCmdTimeout(context.Context, PushTarget, ProbeContext, string) (CmdTimeoutSupport, string, error) {
	return CmdTimeoutAccepted, "", nil
}
