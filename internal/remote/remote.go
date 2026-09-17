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
	"github.com/snonux/gonf/resource"
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

// SSHRunner runs ssh with argv (typically ssh [opts...] host remote-cmd)
// under ctx: canceling ctx (e.g. a fleet abort or per-host timeout) kills the
// in-flight ssh process. A nil ctx is treated as context.Background. A
// context kill is wrapped with the context error so callers can distinguish
// an aborted push (errors.Is(err, context.Canceled / DeadlineExceeded)) from
// the ssh command's own failure. Overridable in tests (including api and cli
// tests, which install fake runners here).
var SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("ssh: empty argv")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		// Killed by the push context (abort or deadline), not an ssh failure.
		return fmt.Errorf("%w (ssh killed by context: %v)", ctx.Err(), err)
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
func PushPayload(t PushTarget, payload []byte, elevate bool, applyDir string) error {
	if t.Host == "" {
		return fmt.Errorf("push: empty host")
	}
	remote, err := remoteApplyCmd(elevate, t, applyDir)
	if err != nil {
		return err
	}
	return SSHRunner(context.Background(), bytes.NewReader(payload), t.sshArgv(remote))
}

// PushChunks splits ops into privilege chunks and streams each chunk to one
// SSH target. The ctx (Background for direct api.PushTo calls; the per-host
// timeout context in the fleet fan-out) kills the in-flight ssh when
// canceled. A ValidateChunkDeps pre-flight runs before any SSH traffic: a
// dep recorded in a later privilege chunk (or dangling) fails the push
// without sending anything, mirroring the privilege pre-flight. Multi-chunk
// plans with blobs first upload all blobs to a sticky dir in a dedicated
// always-unprivileged session (pushBlobs); every chunk then applies
// plan-only with -apply-dir and no embedded blobs. This keeps blob
// extraction owned by the SSH login user even when the first chunk is
// elevated: root could read the blobs anyway, but the login user could not.
//
// The sticky dir's path is deterministic and reused across pushes to the
// same host (a caching benefit: no fresh mkdir/ownership dance every run).
// Reuse only stays safe because cliApplyStdin (internal/cli) wipes the dir's
// CONTENTS before extracting into it — PushChunks itself does not need to
// know or care whether the dir was empty, freshly created, or left over from
// an interrupted prior run; the remote side guarantees a clean extraction
// target either way.
func PushChunks(ctx context.Context, t PushTarget, planID string, ops []plan.Op, mem *plan.MemoryStore) error {
	chunks := plan.SplitPrivilegeChunks(ops)
	if err := validateChunkDeps(chunks); err != nil {
		return err
	}
	hasBlobs := mem != nil && mem.HasBlobs()
	// Sticky dir for multi-chunk plans with blobs: uploaded once, referenced
	// read-only by every chunk. A concrete remote path; the ID is sanitized.
	sticky := ""
	if hasBlobs && len(chunks) > 1 {
		sticky = "/tmp/gonf-apply-sticky-" + sanitizeID(planID)
	}

	// Pre-flight: build every remote apply command before any SSH traffic so
	// a privilege misconfiguration (e.g. -privilege=none with an elevated
	// chunk) fails the push before syncing gonf or sending any chunk.
	remotes := make([]string, len(chunks))
	for i, ch := range chunks {
		applyDir := ""
		if sticky != "" {
			applyDir = sticky
		}
		remote, err := remoteApplyCmd(ch.Elevate, t, applyDir)
		if err != nil {
			return fmt.Errorf("chunk %d: %w", i, err)
		}
		remotes[i] = remote
	}

	installed, err := EnsureRemoteGonf(ctx, t)
	if err != nil {
		return err
	}
	if installed != "" && t.GonfPath == "" {
		t.GonfPath = installed
		// Rebuild remotes so apply uses the freshly installed binary path.
		for i, ch := range chunks {
			applyDir := ""
			if sticky != "" {
				applyDir = sticky
			}
			remote, err := remoteApplyCmd(ch.Elevate, t, applyDir)
			if err != nil {
				return fmt.Errorf("chunk %d: %w", i, err)
			}
			remotes[i] = remote
		}
	}

	if sticky != "" {
		if chunks[0].Ops[0].Op != plan.KindPlan {
			return fmt.Errorf("push: chunk 0 missing plan header")
		}
		if err := pushBlobs(ctx, t, chunks[0].Ops[0], mem, sticky); err != nil {
			// Nothing was applied yet, but the blob upload may have partly
			// landed in the sticky dir before failing. Best-effort removal:
			// even without this, the next push to the same host would still
			// be correct (cliApplyStdin wipes the dir's contents before
			// extracting), but cleaning up now avoids leaking a part-filled
			// dir under /tmp until that next push happens.
			pushRemoveSticky(ctx, t, sticky)
			return err
		}
	}

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
	if sticky != "" {
		pushRemoveSticky(ctx, t, sticky)
	}
	return nil
}

// remoteApplyCmd builds the remote shell command for one apply session.
func remoteApplyCmd(elevate bool, t PushTarget, applyDir string) (string, error) {
	stdinArg := "-"
	if resource.DryRun() {
		stdinArg = "-n -"
	}
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
func pushBlobs(ctx context.Context, t PushTarget, header plan.Op, mem *plan.MemoryStore, applyDir string) error {
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, []plan.Op{header}, mem); err != nil {
		return fmt.Errorf("encode blobs: %w", err)
	}
	remote, err := remoteApplyCmd(false, t, applyDir)
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

// validateChunkDeps runs the plan-level cross-chunk dependency pre-flight
// (plan.ValidateChunkDeps) over the split privilege chunks: forward
// cross-chunk and dangling deps fail before any chunk is applied or
// uploaded. Mirrors the api-side helper of the same name shared by the local
// ApplyChunks engine.
func validateChunkDeps(chunks []plan.Chunk) error {
	bodies := make([][]plan.Op, len(chunks))
	for i, ch := range chunks {
		bodies[i] = ch.Ops
	}
	return plan.ValidateChunkDeps(bodies)
}
