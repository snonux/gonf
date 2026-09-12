package api

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

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

// PushTarget is one SSH destination (inventory optional).
type PushTarget struct {
	User     string
	Host     string // SSH hostname or user@host when User is empty (CLI form)
	Port     int
	Identity string
	ExtraSSH []string // optional raw ssh argv inserted after "ssh"
}

func (t PushTarget) destination() string {
	if t.User != "" {
		return t.User + "@" + t.Host
	}
	return t.Host
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

func remoteApplyCmd() string {
	if resource.DryRun() {
		return "gonf apply -n -"
	}
	return "gonf apply -"
}

// PushPayload streams an already-encoded GONF-PUSH/1 blob to one SSH target.
func PushPayload(t PushTarget, payload []byte) error {
	if t.Host == "" {
		return fmt.Errorf("push: empty host")
	}
	return sshRunner(bytes.NewReader(payload), t.sshArgv(remoteApplyCmd()))
}

// PushTo records tasks into memory, encodes GONF-PUSH/1, and streams over SSH.
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
	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, ops, mem); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if err := PushPayload(t, buf.Bytes()); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "pushed %s (%d ops) to %s\n", planID, len(ops), t.destination())
	return nil
}

func cliPush(args []string) int {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "Remote dry-run (-n on apply)")
	dryRunShort := fs.Bool("n", false, "Alias for -dry-run")
	planID := fs.String("id", "push", "plan id written into the header")

	pushFlags, rest := takePushFlags(args)
	if err := fs.Parse(pushFlags); err != nil {
		return 2
	}
	if len(fs.Args()) > 0 {
		rest = append(fs.Args(), rest...)
	}

	sshOpts, pos := parsePushArgs(rest)
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gonf push [-n|-dry-run] [-id name] [-- ssh-args...] user@host <task> [task...]")
		return 2
	}

	if *dryRun || *dryRunShort {
		resource.SetDryRun(true)
	}

	t := PushTarget{Host: pos[0], ExtraSSH: sshOpts}
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
		case "id":
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
