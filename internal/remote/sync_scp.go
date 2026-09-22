package remote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Copying the gonf binary to a remote host with scp(1): the real SCPRunner
// and the translation of a target's ExtraSSH ssh(1) options into an scp
// argv (allow-, deny- and rewrite rules for the flag letters the two tools
// do or do not share).

// scpPassThroughOptLetters are ssh(1) option letters that carry the exact
// same meaning under scp(1) (verified against both man pages), so their
// ExtraSSH tokens are forwarded to scp unchanged, joined or separate form:
//
//	-o ssh_option   (ssh_config option, e.g. StrictHostKeyChecking)
//	-i identity_file
//	-F configfile
//	-J destination  (ProxyJump)
//	-c cipher_spec
//	-A              (forward ssh-agent — identical meaning in both man pages)
//
// -S used to be listed here too, but it is a letter collision, not a shared
// meaning — see scpRejectedOptLetters.
const scpPassThroughOptLetters = "oiFJcA"

// scpPassThroughTakesValueLetters is the subset of scpPassThroughOptLetters
// that carries an argument in SEPARATE form (e.g. "-o value", not the joined
// "-ovalue"). -A (agent forwarding) is excluded: it is boolean in both ssh
// and scp and never has a following value of its own.
//
// scpArgv's token-classification loop uses this list to decide, purely from
// the CURRENT token, whether the NEXT token is unconditionally that flag's
// value — see the loop's doc comment for why this replaces guessing a
// token's role from its own shape.
const scpPassThroughTakesValueLetters = "oiFJc"

// scpRejectedOptLetters are ssh(1) option letters that scpArgv refuses to
// forward to scp. Three different hazards land here, and none of them is
// "scp will just print usage and exit" — that clean-rejection case would be
// harmless to forward and does not need to live in this list:
//
//   - scp has no such flag at all, so it fails cleanly on an unknown option
//     (-L/-W local/stdio forwarding, -e escape character, -x disable X11
//     forwarding) — listed here anyway so scpArgv's own error message names
//     the ssh meaning the user actually asked for, instead of scp's opaque
//     "unknown option" diagnostic.
//
//   - scp defines the SAME letter with a DIFFERENT, unrelated meaning that
//     silently forwarding would trigger instead of the ssh meaning the user
//     intended:
//
//     -D  ssh: local dynamic port-forward "[bind:]port"
//     scp: connect to a local sftp-server program at the given path
//     -R  ssh: remote port-forward "remote_port:host:hostport"
//     scp: copy between two remote hosts by running scp on the origin host
//     (a boolean flag, no argument — so scp would then choke on ssh's
//     forward spec as a bogus extra file argument)
//     -T  ssh: disable pseudo-terminal allocation (no argument)
//     scp: disable strict server-filename checking (no argument) — a
//     security-relevant behavior change scp would apply silently
//     -S  ssh: ControlPath — path to a local control socket used for
//     connection multiplexing (an opaque string argument)
//     scp: "-S program" — an alternate PROGRAM TO EXECUTE in place of ssh
//     for the underlying transport (also a single opaque string
//     argument, so nothing about the syntax catches the mismatch).
//     Empirically confirmed: "scp -S /nonexistent-program -o
//     ConnectTimeout=1 ..." tries to exec that path as the transport
//     program and fails with a confusing "No such file or directory"
//     — not a clean rejection — exactly the silent-misinterpretation
//     hazard this whole translation layer exists to prevent.
//
//   - scp recognizes the SAME letter as one of its own internal,
//     undocumented legacy-protocol source/sink flags (not in scp's man page
//     SYNOPSIS, but still accepted by the binary), which makes the scp
//     subprocess block waiting for a protocol handshake that will never
//     arrive — it HANGS rather than erroring, so relying on scp to reject it
//     itself is not an option:
//
//     -t  ssh: force pseudo-terminal allocation (no argument)
//     scp: internal "to" (sink) side of the scp protocol
//     -f  ssh has no "-f" option at all
//     scp: internal "from" (source) side of the scp protocol
//
//     Empirically confirmed: both "scp -t foo" and "scp -f foo" hang rather
//     than exit with an error.
//
// Per gonf's task guidance, failing loudly here (reject) beats silently
// dropping, silently reinterpreting, or silently hanging on a flag the user
// explicitly asked for.
const scpRejectedOptLetters = "LRDWetTxSf"

// defaultSCPRunner copies a local file to a remote path via scp. It is
// Pusher's default SCPRunner implementation (see NewPusher); tests override
// a Pusher's SCPRunner field directly instead of this function; inside a
// test binary a real scp is refused (see refuseNetworkExecInTests).
func defaultSCPRunner(ctx context.Context, localPath string, t PushTarget, remotePath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	argv, err := scpArgv(t, localPath, remotePath)
	if err != nil {
		return err
	}
	refuseNetworkExecInTests(argv)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	if runErr != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (scp killed by context: %v)", ctx.Err(), runErr)
	}
	return runErr
}

// scpArgv builds an scp command line for staging the gonf binary. ssh and
// scp share very few flag letters with identical meaning, so ExtraSSH tokens
// are translated or explicitly allow/deny-listed rather than forwarded
// verbatim (see scpPassThroughOptLetters and scpRejectedOptLetters):
//
//   - "-l USER" / "-lUSER" (ssh: login user) has no matching scp letter —
//     scp's own "-l" means bandwidth limit in Kbit/s — so it becomes scp's
//     "-o User=USER" (an ssh_config option scp forwards to the underlying
//     ssh connection).
//   - "-p PORT" / "-pPORT" / "-P PORT" / "-PPORT" (ssh's port option) becomes
//     scp's own "-P PORT" (scp's own "-p" means preserve-mtime, a different
//     flag; scp does not accept ssh's joined "-pPORT" form as a port at all).
//
// An error is returned instead of an argv when ExtraSSH carries one of
// scpRejectedOptLetters, an -l/-p/-P token that doesn't match the expected
// complete pattern (finding 3: a malformed token must not silently bypass
// every check and reach scp verbatim — ExtraSSH is a public field on the
// exported api.PushTarget, so a direct API caller, not just gonf's own CLI
// parser, can hand scpArgv a bare trailing "-l" or a non-numeric "-p"), or
// any other flag letter that is in neither scpPassThroughOptLetters nor
// scpRejectedOptLetters (default-deny for unrecognized flags: finding 1
// showed that an unlisted letter is not safe to assume is a harmless
// pass-through, so an unknown letter is rejected rather than forwarded).
// These error messages deliberately do not start with an "scp:" tag: the
// only caller (EnsureRemoteGonf) already adds that tag once when it wraps
// the error, and a second "scp:" here used to produce a double-prefixed
// "ensure gonf: scp: scp: ExtraSSH option ..." message.
//
// # Classification is by explicit state, never by a value token's own shape
//
// This is the third fix to the same defect class in this function (see the
// git history for 97e0524/058cea1's two earlier patches). Both prior fixes
// tried to guess, from a token's own shape (does it start with "-l"? does
// its second character look like 'p'/'P'/'l'? does it start with '-' at
// all?), whether the token was itself a flag or the value that belongs to a
// PRECEDING separate-form flag. Each fix closed one shape collision and left
// another: a value could coincidentally match the joined-flag shape, and if
// that value itself started with '-' (e.g. ExtraSSH == []string{"-i",
// "-lweird"}, where "-lweird" is meant literally as -i's value, not a
// flag), the a[0]=='-' guard added in the second fix stopped protecting it.
//
// The loop below eliminates the whole class structurally instead of adding
// a fourth shape check: it walks ExtraSSH by INDEX, and the moment a token
// is identified as a flag that takes a value in separate form (-l, -p, -P,
// or a bare -o/-i/-F/-J/-c), it unconditionally consumes t.ExtraSSH[i+1] as
// that flag's value and advances i past it — with NO further classification
// of that value token. A token only ever reaches the flag-shape checks
// (joined-form detection, pass-through/reject/default-deny) when it was NOT
// already consumed as a preceding flag's value. This makes "is this token a
// flag or a value" a property of the LOOP'S STATE (was the previous token a
// value-taking flag?) rather than a guess re-derived from the token's own
// characters on every iteration, so no value's shape — however flag-like —
// can ever cause it to be misclassified.
func scpArgv(t PushTarget, localPath, remotePath string) ([]string, error) {
	argv := []string{"scp"}
	port := t.Port
	for i := 0; i < len(t.ExtraSSH); i++ {
		args, nextPort, skip, err := translateScpArg(t.ExtraSSH, i, port)
		if err != nil {
			return nil, err
		}
		argv = append(argv, args...)
		port = nextPort
		i += skip
	}
	argv = append(argv, "-o", "ConnectTimeout="+sshConnectTimeout)
	if port > 0 {
		argv = append(argv, "-P", strconv.Itoa(port))
	}
	if t.Identity != "" {
		argv = append(argv, "-i", t.Identity)
	}
	return append(argv, localPath, t.Destination()+":"+remotePath), nil
}

// translateScpArg translates the ExtraSSH token extra[i] for scpArgv. The
// results are named because the two ints share a type and are easy to swap
// at a call site: args are the scp tokens to append (nil when the token is
// dropped or folded into the port), nextPort is the port scpArgv carries
// forward (unchanged unless this token set it and no port was set before),
// and skip is how many FOLLOWING tokens were consumed as this token's value
// (0 or 1), which scpArgv adds to its loop index. scpPortValue,
// scpJoinedPortValue and translateScpFlag return the same tuple.
func translateScpArg(extra []string, i, port int) (args []string, nextPort, skip int, err error) {
	a := extra[i]
	if a == "-p" || a == "-P" {
		return scpPortValue(extra, i, a, port)
	}
	if a == "-l" {
		if i+1 >= len(extra) {
			return nil, port, 0, fmt.Errorf(`ExtraSSH option %q is missing a login-user value; want "-l USER"`, a)
		}
		return []string{"-o", "User=" + extra[i+1]}, port, 1, nil
	}
	if len(a) > 2 && a[0] == '-' && (a[1] == 'p' || a[1] == 'P') {
		return scpJoinedPortValue(a, port)
	}
	if len(a) > 2 && a[0] == '-' && a[1] == 'l' {
		return []string{"-o", "User=" + a[2:]}, port, 0, nil
	}
	if len(a) >= 2 && a[0] == '-' {
		return translateScpFlag(extra, i, a, port)
	}
	return []string{a}, port, 0, nil
}

func scpPortValue(extra []string, i int, option string, port int) (args []string, nextPort, skip int, err error) {
	if i+1 >= len(extra) {
		return nil, port, 0, fmt.Errorf(`ExtraSSH option %q is missing a value; want "-p PORT" or "-P PORT"`, option)
	}
	value := extra[i+1]
	p, err := strconv.Atoi(value)
	if err != nil {
		return nil, port, 0, fmt.Errorf(`ExtraSSH option %q has a non-numeric port %q; want "-p PORT" or "-P PORT"`, option, value)
	}
	if port == 0 {
		port = p
	}
	return nil, port, 1, nil
}

func scpJoinedPortValue(option string, port int) (args []string, nextPort, skip int, err error) {
	p, err := strconv.Atoi(option[2:])
	if err != nil {
		return nil, port, 0, fmt.Errorf(`ExtraSSH option %q has a non-numeric port; want "-pPORT" or "-PPORT"`, option)
	}
	if port == 0 {
		port = p
	}
	return nil, port, 0, nil
}

func translateScpFlag(extra []string, i int, option string, port int) (args []string, nextPort, skip int, err error) {
	letter := option[1]
	switch {
	case strings.ContainsRune(scpPassThroughOptLetters, rune(letter)):
		args = []string{option}
		if len(option) != 2 || !strings.ContainsRune(scpPassThroughTakesValueLetters, rune(letter)) {
			return args, port, 0, nil
		}
		if i+1 >= len(extra) {
			return nil, port, 0, fmt.Errorf("ExtraSSH option %q is missing a value", option)
		}
		return append(args, extra[i+1]), port, 1, nil
	case strings.ContainsRune(scpRejectedOptLetters, rune(letter)):
		return nil, port, 0, fmt.Errorf("ExtraSSH option %q is ssh-only, means something different under scp, or would hang the scp subprocess, and cannot be used for the gonf binary sync step; remove it from ExtraSSH", option)
	default:
		return nil, port, 0, fmt.Errorf("ExtraSSH option %q is not a recognized scp option for the gonf binary sync step; add it to scpArgv's allow/deny list if it is genuinely safe, or remove it from ExtraSSH", option)
	}
}
