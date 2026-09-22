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
// only when the binary parses that flag and value. The probe (one ssh round
// trip per needed context) runs only when the controller's timeout differs
// from the built-in default; the common case sends the unchanged argv. A
// remote that does not accept the flag gets the apply without it, and a
// warning says its commands run under their own default; push (which
// upgrades a stale gonf) or reinstall that host's gonf to get the bound.

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
// an error, a binary that does not accept the flag is a warning.
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
// warns when the remote binary does not accept flag. A nil prober (a
// hand-built Pusher) cannot verify support, so nothing is forwarded.
func (p *Pusher) acceptsCmdTimeout(ctx context.Context, t PushTarget, pc ProbeContext, flag string) (bool, error) {
	ok := false
	if p.CmdTimeoutProber != nil {
		var err error
		if ok, err = p.CmdTimeoutProber(ctx, t, pc, flag); err != nil {
			return false, fmt.Errorf("push %s: probe -cmd-timeout support: %w", t.Destination(), err)
		}
	}
	if !ok {
		logger.Warn("push %s: the %s gonf does not accept %s; its apply runs with the remote default command timeout "+
			"(push to upgrade gonf there)", t.Destination(), probeContextLabel(pc), flag)
	}
	return ok, nil
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
// "gonf <flag> -plan-version" in pc and reports whether the output is a plan
// schema integer. A binary without the flag fails flag parsing and prints
// nothing on stdout (sshCapture turns its non-zero exit into empty output),
// and so does a missing binary. Unparseable output (a login banner mixed
// into stdout) is also reported as "not accepted": forwarding stays
// opt-in on positive evidence, and the apply itself still runs.
func probeCmdTimeoutSupport(ctx context.Context, t PushTarget, pc ProbeContext, flag string) (bool, error) {
	cmd, err := remoteProbeCmd(t, pc, flag+" -plan-version")
	if err != nil {
		return false, err
	}
	out, err := sshCapture(ctx, t, cmd)
	if err != nil {
		return false, err
	}
	_, convErr := strconv.Atoi(strings.TrimSpace(out))
	return convErr == nil, nil
}

// acceptCmdTimeout is the fake CmdTimeoutProber installed by the Assume*
// test seams: the remote gonf is current, so it accepts the flag.
func acceptCmdTimeout(context.Context, PushTarget, ProbeContext, string) (bool, error) {
	return true, nil
}
