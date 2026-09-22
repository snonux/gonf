package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// sshConnectTimeout is the ConnectTimeout option appended to every generated
// ssh argv. It bounds only the TCP/SSH handshake: a half-open connection
// (dropped firewall state, wedged host) would otherwise hang the push
// forever. A long remote apply is deliberately NOT bounded by it — applies
// are long by nature; the fleet path adds a per-host timeout on top instead.
// A target needing a different value passes its own ConnectTimeout via
// ExtraSSH or -- ssh-args: ssh uses the first option on the command line, so
// an explicit one wins.
const sshConnectTimeout = "15"

// sshServerAliveInterval/sshServerAliveCountMax make ssh itself detect a
// NETWORK-level hang, not just a hung remote process: ConnectTimeout only
// bounds the initial handshake, and the controller-side per-host timeout
// (DefaultHostTimeout, hostTimeoutCtx in fleet.go) only kills the LOCAL ssh
// client process via its context — a network path that has gone silent
// without tearing down the TCP session (a dropped route, a wedged NAT/
// firewall state) can otherwise leave that local ssh process blocked in a
// read syscall past its own deadline, since exec.CommandContext's kill
// signal still has to be scheduled and delivered by the OS. With
// ServerAlive* set, ssh itself sends periodic keepalives over the already
// encrypted channel and disconnects on its own once sshServerAliveCountMax
// of them go unanswered — an independent, ssh-native detector for exactly
// the failure mode the controller-side timeout cannot always catch quickly.
// 15s * 4 gives a ~60s worst-case detection window, well under
// DefaultHostTimeout.
const (
	sshServerAliveInterval = "15"
	sshServerAliveCountMax = "4"
)

// SSHRunner runs ssh with argv (typically ssh [opts...] host remote-cmd)
// under ctx: canceling ctx (e.g. a fleet abort or per-host timeout) kills the
// in-flight ssh process. A nil ctx is treated as context.Background. A
// context kill is wrapped with the context error so callers can distinguish
// an aborted push (errors.Is(err, context.Canceled / DeadlineExceeded)) from
// the ssh command's own failure. Overridable in tests (including api and cli
// tests, which install fake runners here).
var SSHRunner = defaultSSHRunner

// defaultSSHRunner is SSHRunner's production implementation. Inside a test
// binary it refuses to exec a real ssh (see refuseNetworkExecInTests), so a
// test that forgot to fake SSHRunner fails loudly instead of touching the
// network.
func defaultSSHRunner(ctx context.Context, stdin io.Reader, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("ssh: empty argv")
	}
	refuseNetworkExecInTests(argv)
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	// The remote gonf has no secret registry: its output reaches the
	// controller's terminal through the controller's redactor, line by line;
	// a descendant still holding the pipe delays the return by at most
	// logger.RelayWaitDelay and is then handed to a detached cat writing to
	// stderr (RunRelayed).
	err := logger.RunRelayed(cmd, os.Stderr)
	if err != nil && ctx.Err() != nil {
		// Killed by the push context (abort or deadline), not an ssh failure.
		// Both causes are wrapped: the context error lets the fan-out tell an
		// abort from a failure, and ssh's own exit error stays inspectable.
		return fmt.Errorf("%w (ssh killed by context: %w)", ctx.Err(), err)
	}
	return err
}

// PushTarget is one SSH destination (inventory optional).
type PushTarget struct {
	User      string
	Host      string // SSH hostname or user@host when User is empty (CLI form)
	Port      int
	Identity  string
	ExtraSSH  []string // optional raw ssh argv inserted after "ssh"
	Privilege privilege.Mode
	// GOOS / GOARCH select the cross-compile target when EnsureRemoteGonf
	// upgrades the remote binary. Empty → probe via uname on the host.
	GOOS   string
	GOARCH string
	// GonfPath is the remote install path for synced binaries (default
	// /usr/local/bin/gonf).
	GonfPath string
}

// Destination returns the user@host (or bare host) this target connects to.
// It is what the push summary reports per target.
func (t PushTarget) Destination() string {
	if t.User != "" {
		return t.User + "@" + t.Host
	}
	return t.Host
}

func (t PushTarget) privilegeMode() privilege.Mode {
	return t.Privilege
}

func (t PushTarget) sshArgv(remoteCmd string) []string {
	argv := []string{"ssh"}
	argv = append(argv, t.ExtraSSH...)
	argv = append(argv, "-o", "ConnectTimeout="+sshConnectTimeout)
	argv = append(argv, "-o", "ServerAliveInterval="+sshServerAliveInterval)
	argv = append(argv, "-o", "ServerAliveCountMax="+sshServerAliveCountMax)
	if t.Port > 0 {
		argv = append(argv, "-p", strconv.Itoa(t.Port))
	}
	if t.Identity != "" {
		argv = append(argv, "-i", t.Identity)
	}
	argv = append(argv, t.Destination(), remoteCmd)
	return argv
}

// PushPayload streams an already-encoded GONF-PUSH/1 blob to one SSH target.
// Equivalent to PushPayloadContext(context.Background(), ...); kept as its
// own entry point for existing (context.Background()-rooted) callers.
func PushPayload(t PushTarget, payload []byte, elevate bool, applyDir string) error {
	return PushPayloadContext(context.Background(), t, payload, elevate, applyDir)
}

// PushPayloadContext is PushPayload bounded/cancelable by ctx: canceling ctx
// kills the in-flight ssh process. When ctx has no deadline of its own,
// DefaultHostTimeout is applied so a wedged remote command cannot hang this
// one-shot push forever.
func PushPayloadContext(ctx context.Context, t PushTarget, payload []byte, elevate bool, applyDir string) error {
	if t.Host == "" {
		return fmt.Errorf("push: empty host")
	}
	remote, err := remoteApplyCmd(elevate, t, applyDir, Push)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultHostTimeout)
		defer cancel()
	}
	return SSHRunner(ctx, bytes.NewReader(payload), t.sshArgv(remote))
}

// requireRemoteGonfForChunks verifies every privilege context that will run a
// strict-preview apply. The same path can resolve to different binaries for
// the SSH user and sudo/doas, so mixed plans require both probes. The context
// is passed to RequireRemoteGonf explicitly (ProbeLogin / ProbeElevated); t
// itself stays the plain destination.
func requireRemoteGonfForChunks(ctx context.Context, t PushTarget, chunks []plan.Chunk) error {
	var needUnprivileged, needElevated bool
	for _, chunk := range chunks {
		if chunk.Elevate {
			needElevated = true
		} else {
			needUnprivileged = true
		}
	}
	if needUnprivileged {
		if err := RequireRemoteGonf(ctx, t, ProbeLogin); err != nil {
			return err
		}
	}
	if needElevated {
		if err := RequireRemoteGonf(ctx, t, ProbeElevated); err != nil {
			return err
		}
	}
	return nil
}

// buildRemoteCmds builds the remote apply command for every chunk, in order,
// in the given delivery mode. Called once up front (pre-flight, before any
// SSH traffic) and again after EnsureRemoteGonf if it installed a fresh
// binary at a new path, so both passes share one implementation instead of
// drifting apart. sticky ("" for none) is every chunk's -apply-dir.
func buildRemoteCmds(chunks []plan.Chunk, t PushTarget, sticky string, mode Mode) ([]string, error) {
	remotes := make([]string, len(chunks))
	for i, ch := range chunks {
		remote, err := remoteApplyCmd(ch.Elevate, t, sticky, mode)
		if err != nil {
			return nil, fmt.Errorf("chunk %d: %w", i, err)
		}
		remotes[i] = remote
	}
	return remotes, nil
}

// uploadSticky uploads every blob to the sticky apply dir before any chunk
// applies (see pushBlobs). On failure it best-effort removes whatever
// partly landed in the sticky dir: nothing was applied yet, but leaving a
// part-filled dir under /tmp would linger until the next push to the same
// host (cliApplyStdin wipes it before extracting, so this is cleanup, not
// correctness).
func uploadSticky(ctx context.Context, t PushTarget, chunks []plan.Chunk, mem plan.BlobReader, sticky string) error {
	if chunks[0].Ops[0].Op != plan.KindPlan {
		return fmt.Errorf("push: chunk 0 missing plan header")
	}
	if err := pushBlobs(ctx, t, chunks[0].Ops[0], mem, sticky); err != nil {
		pushRemoveSticky(ctx, t, sticky)
		return err
	}
	return nil
}

// streamChunks encodes and streams every chunk to t over SSH, in order.
// When sticky is set, blobs were already uploaded by uploadSticky, so each
// chunk is encoded plan-only (chunkMem nil). A mid-stream failure removes
// the sticky dir (its blobs were consumed or are now unusable) and reports
// how many ops from earlier chunks already landed on the host, without
// masking the underlying error.
func streamChunks(ctx context.Context, t PushTarget, chunks []plan.Chunk, remotes []string, mem plan.BlobReader, sticky string) error {
	for i, ch := range chunks {
		chunkMem := mem
		if sticky != "" {
			chunkMem = nil // plan-only; blobs already in the sticky dir
		}
		var buf bytes.Buffer
		if err := plan.EncodePush(&buf, ch.Ops, chunkMem); err != nil {
			return fmt.Errorf("encode chunk %d: %w", i, err)
		}
		if err := SSHRunner(ctx, bytes.NewReader(buf.Bytes()), t.sshArgv(remotes[i])); err != nil {
			err = fmt.Errorf("chunk %d (elevate=%v): %w", i, ch.Elevate, err)
			if len(chunks) > 1 && i > 0 {
				// Any chunk failure after the first leaves the host partially
				// applied: earlier chunks already ran.
				applied := 0
				for _, prev := range chunks[:i] {
					applied += len(prev.Ops) - 1 // minus the header
				}
				err = fmt.Errorf("%w (host left partially applied: %d ops from %d earlier chunks)", err, applied, i)
			}
			if sticky != "" {
				// The sticky dir is no longer needed: its blobs were consumed
				// or are now unusable. Best-effort removal, never masking the
				// chunk failure.
				pushRemoveSticky(ctx, t, sticky)
			}
			return err
		}
	}
	return nil
}

// remoteApplyCmd builds the remote shell command for one apply session in
// the given delivery mode (Mode.applyStdinArg picks the stdin argument).
func remoteApplyCmd(elevate bool, t PushTarget, applyDir string, mode Mode) (string, error) {
	stdinArg := mode.applyStdinArg()
	args := "apply " + stdinArg
	if applyDir != "" {
		args = "apply -apply-dir " + applyDir + " " + stdinArg
	}
	return privilege.WrapApplyBinCmd(t.privilegeMode(), elevate, remoteGonfBin(t), args)
}

// pushRemoveStickyTimeout bounds the best-effort sticky-dir removal below.
// It runs on a context that has deliberately been detached from the push's
// own cancellation (see pushRemoveSticky), so it needs its own short bound
// instead of being able to hang forever against an unreachable host.
const pushRemoveStickyTimeout = 15 * time.Second

// pushRemoveSticky best-effort removes the remote sticky apply dir after the
// last apply chunk (or on failure): leftover blob staging would accumulate
// forever under /tmp — nothing else sweeps it (unlike the non-sticky
// NewApplyRunDir staging root, which plan.SweepApplyStaging ages out).
// The dir is owned by the SSH login user, so the removal runs unprivileged;
// a failure (e.g. a stale root-owned dir from older gonf versions) is logged
// and never fails the push.
//
// It deliberately does NOT run on ctx (the push's own context) directly: by
// the time cleanup runs, ctx may already be canceled or expired — SIGINT,
// -host-timeout, or a sibling host's failure in Fanout triggering
// errgroup-wide cancellation all cancel it before this point. An already-
// canceled/expired context makes exec.CommandContext refuse to even start
// the process (see SSHRunner), so cleanup would silently never happen,
// leaking the sticky dir and setting up the NEXT push to this host for the
// staleness this fix closes (cliApplyStdin's wipe-before-extract in
// internal/cli is the other half: it tolerates a leaked dir, but cleanup
// here still runs whenever it can). context.WithoutCancel detaches from
// ctx's cancellation/deadline while keeping its values, and the fresh
// timeout keeps this best-effort call from hanging forever on an
// unreachable host.
func pushRemoveSticky(ctx context.Context, t PushTarget, sticky string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pushRemoveStickyTimeout)
	defer cancel()
	remote := "rm -rf " + sticky
	if err := SSHRunner(cleanupCtx, bytes.NewReader(nil), t.sshArgv(remote)); err != nil {
		logger.Warn("push: failed to remove remote sticky dir %s: %v", sticky, err)
	}
}

// pushBlobs uploads all plan blobs to the sticky apply dir over SSH before any
// apply chunk runs. The frame is a regular GONF-PUSH/1 stream with the blobs
// tar attached and a header-only plan, so the remote extracts the blobs as the
// SSH login user and then applies an empty plan (no-op). Never privilege-
// wrapped: elevated apply chunks read the blobs as root later on.
func pushBlobs(ctx context.Context, t PushTarget, header plan.Op, mem plan.BlobReader, applyDir string) error {
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, []plan.Op{header}, mem); err != nil {
		return fmt.Errorf("encode blobs: %w", err)
	}
	remote, err := remoteApplyCmd(false, t, applyDir, Push)
	if err != nil {
		return err
	}
	if err := SSHRunner(ctx, bytes.NewReader(buf.Bytes()), t.sshArgv(remote)); err != nil {
		return fmt.Errorf("blob upload to %s: %w", applyDir, err)
	}
	return nil
}

// sanitizeID reduces a plan ID to ssh- and path-safe characters.
func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "plan"
	}
	return b.String()
}
