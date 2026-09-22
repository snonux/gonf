package remote

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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
// Any verdict other than "accepted" leaves the flag out, so the apply runs
// under the remote's own default command timeout, and never fails the push
// (only an ssh failure does). The warning says why: a binary too old for the
// flag (push, which upgrades a stale gonf, or reinstall it) differs from a
// probe that could not run gonf at all (sudo/doas refused it, gonf missing).

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
// needed privilege context (see the file comment); only an ssh failure is
// an error, any other verdict than "accepted" is a warning.
func (p *Pusher) resolveCmdTimeoutForward(ctx context.Context, t PushTarget, needLogin, needElevated bool) (cmdTimeoutForward, error) {
	f := cmdTimeoutForward{flag: gexec.CmdTimeoutFlag(gexec.DefaultTimeout())}
	if f.flag == "" {
		return f, nil
	}
	var err error
	if needLogin {
		if f.login, err = p.acceptsCmdTimeout(ctx, t, ProbeLogin, f.flag); err != nil {
			return cmdTimeoutForward{}, err
		}
	}
	if needElevated {
		if f.elevated, err = p.acceptsCmdTimeout(ctx, t, ProbeElevated, f.flag); err != nil {
			return cmdTimeoutForward{}, err
		}
	}
	return f, nil
}

// acceptsCmdTimeout runs p.CmdTimeoutProber for one privilege context and
// warns, worded by verdict, when the flag will not be forwarded there. A
// nil prober (a hand-built Pusher) cannot verify support, so nothing is
// forwarded.
func (p *Pusher) acceptsCmdTimeout(ctx context.Context, t PushTarget, pc ProbeContext, flag string) (bool, error) {
	verdict, detail := CmdTimeoutUnverified, "no capability prober"
	if p.CmdTimeoutProber != nil {
		var err error
		if verdict, detail, err = p.CmdTimeoutProber(ctx, t, pc, flag); err != nil {
			return false, fmt.Errorf("push %s: probe -cmd-timeout support: %w", t.Destination(), err)
		}
	}
	switch verdict {
	case CmdTimeoutAccepted:
		return true, nil
	case CmdTimeoutUnknownFlag:
		logger.Warn("push %s: the %s gonf is too old for %s; its apply runs with the remote default command timeout "+
			"(push to upgrade gonf there)", t.Destination(), probeContextLabel(pc), flag)
	default:
		logger.Warn("push %s: could not run gonf in the %s context to check %s support (%s); its apply runs "+
			"without it, with the remote default command timeout", t.Destination(), probeContextLabel(pc), flag, detail)
	}
	return false, nil
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
// ending) is an error.
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

// firstLine returns s's first non-empty line, trimmed and capped at 200
// bytes so a long error cannot flood the warning.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 200 {
				line = line[:200] + "..."
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
