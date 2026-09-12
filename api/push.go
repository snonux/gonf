package api

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
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
		// Defensive: takePushFlags should leave no positionals in pushFlags.
		rest = append(fs.Args(), rest...)
	}

	sshOpts, pos := parsePushArgs(rest)
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gonf push [-n|-dry-run] [-id name] [-- ssh-args...] user@host <task> [task...]")
		return 2
	}
	host := pos[0]
	tasks := pos[1:]

	if *dryRun || *dryRunShort {
		resource.SetDryRun(true)
	}

	mem := plan.NewMemoryStore()
	ops, err := RecordPlanTo(*planID, mem, tasks...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "push: record: %v\n", err)
		return 1
	}

	var buf bytes.Buffer
	if err := plan.EncodePush(&buf, ops, mem); err != nil {
		fmt.Fprintf(os.Stderr, "push: encode: %v\n", err)
		return 1
	}

	remote := "gonf apply -"
	if resource.DryRun() {
		remote = "gonf apply -n -"
	}
	argv := []string{"ssh"}
	argv = append(argv, sshOpts...)
	argv = append(argv, host, remote)

	if err := sshRunner(&buf, argv); err != nil {
		fmt.Fprintf(os.Stderr, "push: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "pushed %s (%d ops) to %s\n", *planID, len(ops), host)
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
			// Unknown dash arg → ssh opts / host (do not feed to FlagSet).
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
// Accepts either an explicit "--" separator or leading -opts (flag.Parse strips "--").
// Example: -p 2222 user@host task → sshOpts=-p 2222, pos=user@host task
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
