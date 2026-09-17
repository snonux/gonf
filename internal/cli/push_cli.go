package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/resource"
)

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

	// Escalate-only: a top-level "gonf -n push ..." already set this via
	// CLI()'s unconditional call before dispatch (or a caller pre-set the
	// global directly); don't stomp it back to false just because this
	// subcommand's own flags didn't repeat -n.
	if *dryRun || *dryRunShort {
		resource.SetDryRun(true)
	}
	mode := api.Privilege()
	if *privFlag != "" {
		m, err := privilege.ParseMode(*privFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "push: %v\n", err)
			return 2
		}
		mode = m
	}

	t := api.PushTarget{Host: pos[0], ExtraSSH: sshOpts, Privilege: mode}
	if err := api.PushTo(t, *planID, pos[1:]...); err != nil {
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
