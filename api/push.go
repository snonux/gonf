package api

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

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

// sshRunner runs ssh with argv (typically ssh [opts...] host remote-cmd)
// under ctx: canceling ctx (e.g. a fleet abort or per-host timeout) kills the
// in-flight ssh process. A nil ctx is treated as context.Background. A
// context kill is wrapped with the context error so callers can distinguish
// an aborted push (errors.Is(err, context.Canceled / DeadlineExceeded)) from
// the ssh command's own failure. Overridable in tests.
var sshRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
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

// processPrivilege is the CLI/local default for wrapping privileged chunks.
var processPrivilege = privilege.None

// SetPrivilege sets the process-wide privilege helper (CLI -privilege).
func SetPrivilege(mode privilege.Mode) { processPrivilege = mode }

// Privilege returns the process-wide privilege helper.
func Privilege() privilege.Mode { return processPrivilege }

// PushTarget is one SSH destination (inventory optional).
type PushTarget struct {
	User      string
	Host      string // SSH hostname or user@host when User is empty (CLI form)
	Port      int
	Identity  string
	ExtraSSH  []string // optional raw ssh argv inserted after "ssh"
	Privilege privilege.Mode
}

func (t PushTarget) destination() string {
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
	argv = append(argv, t.destination(), remoteCmd)
	return argv
}

func remoteApplyCmd(elevate bool, mode privilege.Mode, applyDir string) (string, error) {
	stdinArg := "-"
	if resource.DryRun() {
		stdinArg = "-n -"
	}
	args := "apply " + stdinArg
	if applyDir != "" {
		args = "apply -apply-dir " + applyDir + " " + stdinArg
	}
	return privilege.WrapApplyCmd(mode, elevate, args)
}

// PushPayload streams an already-encoded GONF-PUSH/1 blob to one SSH target.
func PushPayload(t PushTarget, payload []byte, elevate bool, applyDir string) error {
	if t.Host == "" {
		return fmt.Errorf("push: empty host")
	}
	remote, err := remoteApplyCmd(elevate, t.privilegeMode(), applyDir)
	if err != nil {
		return err
	}
	return sshRunner(context.Background(), bytes.NewReader(payload), t.sshArgv(remote))
}

// PushTo records tasks, splits privilege chunks, and streams each chunk over
// SSH. Single-target pushes run without a controller-managed context (only
// the fleet fan-out is signal-cancellable); the ssh handshake is still
// bounded by the generated ConnectTimeout.
func PushTo(t PushTarget, planID string, tasks ...string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("push: no tasks")
	}
	if planID == "" {
		planID = "push"
	}
	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(planID, mem, tasks...)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := pushChunks(context.Background(), t, planID, ops, mem); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "pushed %s (%d ops) to %s\n", planID, len(ops), t.destination())
	return nil
}

// pushChunks splits ops into privilege chunks and streams each chunk to one
// SSH target. The ctx (Background for direct PushTo calls; the per-host
// timeout context in the fleet fan-out) kills the in-flight ssh when
// canceled. A ValidateChunkDeps pre-flight runs before any SSH traffic: a
// dep recorded in a later privilege chunk (or dangling) fails the push
// without sending anything, mirroring the privilege pre-flight. Multi-chunk
// plans with blobs first upload all blobs to a sticky dir in a dedicated
// always-unprivileged session (pushBlobs); every chunk then applies
// plan-only with -apply-dir and no embedded blobs. This keeps blob
// extraction owned by the SSH login user even when the first chunk is
// elevated: root could read the blobs anyway, but the login user could not.
func pushChunks(ctx context.Context, t PushTarget, planID string, ops []plan.Op, mem *plan.MemoryStore) error {
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
	// chunk) fails the push before sending any chunk or blob upload.
	remotes := make([]string, len(chunks))
	for i, ch := range chunks {
		applyDir := ""
		if sticky != "" {
			applyDir = sticky
		}
		remote, err := remoteApplyCmd(ch.Elevate, t.privilegeMode(), applyDir)
		if err != nil {
			return fmt.Errorf("chunk %d: %w", i, err)
		}
		remotes[i] = remote
	}

	if sticky != "" {
		if chunks[0].Ops[0].Op != plan.KindPlan {
			return fmt.Errorf("push: chunk 0 missing plan header")
		}
		if err := pushBlobs(ctx, t, chunks[0].Ops[0], mem, sticky); err != nil {
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
		if err := sshRunner(ctx, bytes.NewReader(buf.Bytes()), t.sshArgv(remotes[i])); err != nil {
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

// pushRemoveSticky best-effort removes the remote sticky apply dir after the
// last apply chunk (or on failure): leftover blob staging would accumulate
// forever under /tmp. The dir is owned by the SSH login user, so the removal
// runs unprivileged; a failure (e.g. a stale root-owned dir from older gonf
// versions) is logged and never fails the push.
func pushRemoveSticky(ctx context.Context, t PushTarget, sticky string) {
	remote := "rm -rf " + sticky
	if err := sshRunner(ctx, bytes.NewReader(nil), t.sshArgv(remote)); err != nil {
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
	remote, err := remoteApplyCmd(false, t.privilegeMode(), applyDir)
	if err != nil {
		return err
	}
	if err := sshRunner(ctx, bytes.NewReader(buf.Bytes()), t.sshArgv(remote)); err != nil {
		return fmt.Errorf("blob upload to %s: %w", applyDir, err)
	}
	return nil
}

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

func cliPush(args []string) int {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "Remote dry-run (-n on apply)")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	planID := fs.String("id", "push", "plan id written into the header")
	privFlag := fs.String("privilege", "", "none|sudo|doas for privileged chunks")

	pushFlags, rest := takePushFlags(args)
	if err := fs.Parse(pushFlags); err != nil {
		return 2
	}
	if len(fs.Args()) > 0 {
		rest = append(fs.Args(), rest...)
	}

	sshOpts, pos := parsePushArgs(rest)
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gonf push [-n|-dry-run] [-id name] [-privilege=sudo|doas|none] [-- ssh-args...] user@host <task> [task...]")
		return 2
	}

	if *dryRun || *dryRunShort {
		resource.SetDryRun(true)
	}
	mode := processPrivilege
	if *privFlag != "" {
		m, err := privilege.ParseMode(*privFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "push: %v\n", err)
			return 2
		}
		mode = m
	}

	t := PushTarget{Host: pos[0], ExtraSSH: sshOpts, Privilege: mode}
	if err := PushTo(t, *planID, pos[1:]...); err != nil {
		fmt.Fprintf(os.Stderr, "push: %v\n", err)
		return 1
	}
	return 0
}

// takePushFlags peels only gonf-push flags so ssh opts like -p are not
// rejected by flag.Parse. Stops at "--", a non-flag, or an unknown -flag.
func takePushFlags(args []string) (pushFlags, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			return args[:i], args[i+1:]
		}
		if a == "" || a[0] != '-' {
			break
		}
		name, hasVal, _ := splitFlagToken(a)
		switch name {
		case "n", "dry-run":
			pushFlags = append(pushFlags, a)
			i++
		case "id", "privilege":
			if hasVal {
				pushFlags = append(pushFlags, a)
				i++
				break
			}
			if i+1 >= len(args) {
				return append(pushFlags, a), nil
			}
			pushFlags = append(pushFlags, a, args[i+1])
			i += 2
		default:
			return pushFlags, args[i:]
		}
	}
	return pushFlags, args[i:]
}

func splitFlagToken(a string) (name string, hasVal bool, val string) {
	a = strings.TrimLeft(a, "-")
	if i := strings.IndexByte(a, '='); i >= 0 {
		return a[:i], true, a[i+1:]
	}
	return a, false, ""
}

// parsePushArgs splits optional ssh opts then host tasks.
func parsePushArgs(args []string) (sshOpts, pos []string) {
	if i := indexOf(args, "--"); i >= 0 {
		return splitSSHOptsAndPositional(args[i+1:])
	}
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] == '-' {
		return splitSSHOptsAndPositional(args)
	}
	return nil, args
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

func splitSSHOptsAndPositional(args []string) (sshOpts, pos []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "" || a[0] != '-' {
			break
		}
		sshOpts = append(sshOpts, a)
		i++
		if len(a) == 2 && sshOptTakesValue(a[1]) && i < len(args) && (args[i] == "" || args[i][0] != '-') {
			sshOpts = append(sshOpts, args[i])
			i++
		}
	}
	return sshOpts, args[i:]
}

func sshOptTakesValue(b byte) bool {
	return strings.ContainsRune("pilFoJcDLRWbeS", rune(b))
}
