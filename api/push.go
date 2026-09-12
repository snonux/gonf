package api

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
)

// sshRunner runs ssh with argv (typically ssh [opts...] host remote-cmd).
// Overridable in tests.
var sshRunner = func(stdin io.Reader, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("ssh: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
	args := "apply -"
	if resource.DryRun() {
		args = "apply -n -"
	}
	if applyDir != "" {
		args = "apply -apply-dir " + applyDir + " " + strings.TrimPrefix(args, "apply ")
		// produce: apply -apply-dir DIR -   or apply -apply-dir DIR -n -
		if resource.DryRun() {
			args = "apply -apply-dir " + applyDir + " -n -"
		} else {
			args = "apply -apply-dir " + applyDir + " -"
		}
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
	return sshRunner(bytes.NewReader(payload), t.sshArgv(remote))
}

// PushTo records tasks, splits privilege chunks, and streams each chunk over SSH.
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
	if err := pushChunks(t, planID, ops, mem); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "pushed %s (%d ops) to %s\n", planID, len(ops), t.destination())
	return nil
}

func pushChunks(t PushTarget, planID string, ops []plan.Op, mem *plan.MemoryStore) error {
	chunks := plan.SplitPrivilegeChunks(ops)
	hasBlobs := mem != nil && mem.HasBlobs()
	sticky := ""
	if hasBlobs && len(chunks) > 1 {
		sticky = filepath.Join("${TMPDIR:-/tmp}", "gonf-apply-sticky", planID)
		// Use a concrete remote path; expand TMPDIR on remote via shell.
		sticky = "/tmp/gonf-apply-sticky-" + sanitizeID(planID)
	}

	for i, ch := range chunks {
		chunkMem := mem
		applyDir := ""
		keep := false
		if hasBlobs {
			if i == 0 {
				applyDir = sticky
				keep = len(chunks) > 1 && sticky != ""
			} else {
				chunkMem = nil // plan-only; blobs already on remote
				applyDir = sticky
			}
		}
		var buf bytes.Buffer
		if err := plan.EncodePush(&buf, ch.Ops, chunkMem); err != nil {
			return fmt.Errorf("encode chunk %d: %w", i, err)
		}
		remote, err := remoteApplyCmd(ch.Elevate, t.privilegeMode(), applyDir)
		if err != nil {
			return err
		}
		if keep && applyDir != "" {
			// First chunk: ensure dir exists and ask apply not to wipe it.
			remote = "mkdir -p " + applyDir + " && " + remote
		}
		_ = keep
		if err := sshRunner(bytes.NewReader(buf.Bytes()), t.sshArgv(remote)); err != nil {
			return fmt.Errorf("chunk %d (elevate=%v): %w", i, ch.Elevate, err)
		}
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
