package remote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

// gonfCmdPackage is built for remote hosts when their plan schema is too old.
const gonfCmdPackage = "github.com/snonux/gonf/cmd/gonf"

var (
	gonfBuildMu    sync.Mutex
	gonfBuildCache = map[string]string{} // "goos/goarch" → local binary path
)

// SCPRunner copies a local file to a remote path via scp. Overridable in tests.
var SCPRunner = func(ctx context.Context, localPath string, t PushTarget, remotePath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	argv, err := scpArgv(t, localPath, remotePath)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	if runErr != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (scp killed by context: %v)", ctx.Err(), runErr)
	}
	return runErr
}

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
	n := len(t.ExtraSSH)
	for i := 0; i < n; i++ {
		a := t.ExtraSSH[i]

		// Separate-form "-p PORT" / "-P PORT" -> scp's "-P PORT". Because
		// this branch matches on the CURRENT token being exactly "-p"/"-P",
		// the following token is unconditionally consumed as the port value
		// below — it is never itself run through any flag-shape check, no
		// matter what it looks like.
		if a == "-p" || a == "-P" {
			if i+1 >= n {
				return nil, fmt.Errorf(`ExtraSSH option %q is missing a value; want "-p PORT" or "-P PORT"`, a)
			}
			val := t.ExtraSSH[i+1]
			i++
			p, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf(`ExtraSSH option %q has a non-numeric port %q; want "-p PORT" or "-P PORT"`, a, val)
			}
			if port == 0 {
				port = p
			}
			continue
		}
		// Separate-form "-l USER" -> scp's "-o User=USER". Same unconditional
		// lookahead-consumption as above.
		if a == "-l" {
			if i+1 >= n {
				return nil, fmt.Errorf(`ExtraSSH option %q is missing a login-user value; want "-l USER"`, a)
			}
			argv = append(argv, "-o", "User="+t.ExtraSSH[i+1])
			i++
			continue
		}

		// Joined "-pPORT" / "-PPORT" -> scp's "-P PORT". This only matches a
		// token that is COMPLETE in itself (len(a) > 2, starts with '-'), so
		// there is no following value to consume or misclassify.
		if len(a) > 2 && a[0] == '-' && (a[1] == 'p' || a[1] == 'P') {
			p, err := strconv.Atoi(a[2:])
			if err != nil {
				return nil, fmt.Errorf(`ExtraSSH option %q has a non-numeric port; want "-pPORT" or "-PPORT"`, a)
			}
			if port == 0 {
				port = p
			}
			continue
		}
		// Joined "-lUSER" -> scp's "-o User=USER". Likewise complete in
		// itself: nothing follows it to consume.
		if len(a) > 2 && a[0] == '-' && a[1] == 'l' {
			argv = append(argv, "-o", "User="+a[2:])
			continue
		}

		if len(a) >= 2 && a[0] == '-' {
			letter := a[1]
			switch {
			case strings.ContainsRune(scpPassThroughOptLetters, rune(letter)):
				argv = append(argv, a)
				// Bare separate-form flag (e.g. "-o", not the joined
				// "-oFoo=Bar") whose letter takes a value: the very next
				// ExtraSSH token is unconditionally that value and is
				// appended as-is, with no flag-shape check of its own —
				// this is what stops a value like "-lweird" or
				// "-p2222lookalike" from ever being reinterpreted as a
				// flag, regardless of what it starts with or looks like.
				if len(a) == 2 && strings.ContainsRune(scpPassThroughTakesValueLetters, rune(letter)) {
					if i+1 >= n {
						return nil, fmt.Errorf("ExtraSSH option %q is missing a value", a)
					}
					argv = append(argv, t.ExtraSSH[i+1])
					i++
				}
				continue
			case strings.ContainsRune(scpRejectedOptLetters, rune(letter)):
				return nil, fmt.Errorf("ExtraSSH option %q is ssh-only, means something different under scp, or would hang the scp subprocess, and cannot be used for the gonf binary sync step; remove it from ExtraSSH", a)
			default:
				// Default-deny: a flag letter that is neither an explicit
				// pass-through nor an explicit rejection is not known to be
				// safe to forward (see the scpPassThroughOptLetters doc
				// comment for why "unrecognized" cannot be assumed to mean
				// "harmless").
				return nil, fmt.Errorf("ExtraSSH option %q is not a recognized scp option for the gonf binary sync step; add it to scpArgv's allow/deny list if it is genuinely safe, or remove it from ExtraSSH", a)
			}
		}

		// A token that both (a) does not start with '-' and (b) was not
		// already consumed above as a preceding flag's value should not
		// normally occur in well-formed ExtraSSH, but there is nothing else
		// it could be — forward it verbatim rather than silently dropping
		// it.
		argv = append(argv, a)
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

// GoBuildRunner cross-compiles a package. Overridable in tests.
var GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, pkg)
	cmd.Env = append(os.Environ(),
		"GOOS="+goos,
		"GOARCH="+goarch,
		"CGO_ENABLED=0",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// PlanVersionProber reads the remote plan schema version. Tests replace this
// (see AssumeRemotePlanCurrent) so EnsureRemoteGonf does not open a real SSH
// session when SSHRunner is stubbed.
var PlanVersionProber = probePlanVersion

// AssumeRemotePlanCurrent stubs PlanVersionProber so EnsureRemoteGonf skips
// the upgrade path. Restore with the returned func (or t.Cleanup).
func AssumeRemotePlanCurrent() func() {
	old := PlanVersionProber
	PlanVersionProber = func(context.Context, PushTarget) (int, error) {
		return plan.CurrentVersion, nil
	}
	return func() { PlanVersionProber = old }
}

// EnsureRemoteGonf upgrades the remote gonf binary when it cannot apply the
// controller's plan schema (missing gonf, or -plan-version < CurrentVersion).
// GOOS/GOARCH come from the target when set, otherwise from remote uname.
// When an upgrade runs, installedPath is the remote binary path to use for
// subsequent apply commands; otherwise it is empty (keep PATH "gonf").
func EnsureRemoteGonf(ctx context.Context, t PushTarget) (installedPath string, err error) {
	if t.Host == "" {
		return "", fmt.Errorf("ensure gonf: empty host")
	}
	remoteVer, err := PlanVersionProber(ctx, t)
	if err != nil {
		return "", err
	}
	if remoteVer >= plan.CurrentVersion {
		return "", nil
	}
	logger.Info("push %s: remote plan schema %d < %d — syncing gonf binary",
		t.Destination(), remoteVer, plan.CurrentVersion)

	goos, goarch := t.GOOS, t.GOARCH
	if goos == "" || goarch == "" {
		detectedOS, detectedArch, err := probeUname(ctx, t)
		if err != nil {
			return "", fmt.Errorf("ensure gonf: %w", err)
		}
		if goos == "" {
			goos = detectedOS
		}
		if goarch == "" {
			goarch = detectedArch
		}
	}

	localBin, err := buildGonf(ctx, goos, goarch)
	if err != nil {
		return "", fmt.Errorf("ensure gonf: build %s/%s: %w", goos, goarch, err)
	}

	// Stage the binary under a remote directory that mktemp creates
	// exclusively (mode 0700, owned by the SSH login user): unlike the old
	// fixed "/tmp/gonf.new.<pid>" path, a local attacker on a shared host
	// cannot pre-create it as a symlink or a world-writable file, and cannot
	// read or swap its contents between the scp and the install step. The
	// directory name's random suffix also means two concurrent pushes to the
	// same host (even from the same controller PID) never collide.
	remoteDir, err := createRemoteStagingDir(ctx, t)
	if err != nil {
		return "", fmt.Errorf("ensure gonf: mktemp: %w", err)
	}
	defer removeRemoteStagingDir(ctx, t, remoteDir)

	remoteTmp := remoteDir + "/gonf"
	if err := SCPRunner(ctx, localBin, t, remoteTmp); err != nil {
		// scpArgv's own errors do not repeat the "scp:" tag added here (it
		// used to, producing a double-prefixed "ensure gonf: scp: scp:
		// ExtraSSH option ..." message); a real scp(1) exec failure still
		// gets tagged exactly once, here.
		return "", fmt.Errorf("ensure gonf: scp: %w", err)
	}

	installPath := t.GonfPath
	if installPath == "" {
		installPath = "/usr/local/bin/gonf"
	}
	installCmd, err := remoteInstallCmd(t, remoteTmp, installPath)
	if err != nil {
		return "", err
	}
	if err := SSHRunner(ctx, bytes.NewReader(nil), t.sshArgv(installCmd)); err != nil {
		return "", fmt.Errorf("ensure gonf: install: %w", err)
	}

	// Verify via the installed path so PATH order cannot hide an older binary.
	verify := t
	verify.GonfPath = installPath
	got, err := probePlanVersion(ctx, verify)
	if err != nil {
		return "", fmt.Errorf("ensure gonf: verify: %w", err)
	}
	if got < plan.CurrentVersion {
		return "", fmt.Errorf("ensure gonf: remote still reports plan schema %d after install (want ≥ %d)", got, plan.CurrentVersion)
	}
	logger.Info("push %s: remote gonf plan schema now %d", t.Destination(), got)
	return installPath, nil
}

func probePlanVersion(ctx context.Context, t PushTarget) (int, error) {
	bin := remoteGonfBin(t)
	// No "2>/dev/null || true": FreeBSD login shells are often tcsh, which
	// mishandles that idiom and yields empty stdout even when gonf works.
	// sshCapture already treats a remote non-zero exit (missing binary) as
	// empty stdout without failing the SSH session.
	out, err := sshCapture(ctx, t, bin+" -plan-version")
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(line)
	if err != nil {
		return 0, nil // garbage → upgrade
	}
	return n, nil
}

func remoteGonfBin(t PushTarget) string {
	if t.GonfPath != "" {
		return t.GonfPath
	}
	return "gonf"
}

func probeUname(ctx context.Context, t PushTarget) (goos, goarch string, err error) {
	out, err := sshCapture(ctx, t, "uname -s; uname -m")
	if err != nil {
		return "", "", fmt.Errorf("uname: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return "", "", fmt.Errorf("uname: unexpected output %q", out)
	}
	goos, err = mapUnameGOOS(strings.TrimSpace(lines[0]))
	if err != nil {
		return "", "", err
	}
	goarch, err = mapUnameGOARCH(strings.TrimSpace(lines[1]))
	if err != nil {
		return "", "", err
	}
	return goos, goarch, nil
}

func mapUnameGOOS(s string) (string, error) {
	switch strings.ToLower(s) {
	case "linux":
		return "linux", nil
	case "openbsd":
		return "openbsd", nil
	case "netbsd":
		return "netbsd", nil
	case "freebsd":
		return "freebsd", nil
	case "darwin":
		return "darwin", nil
	default:
		return "", fmt.Errorf("unsupported uname -s %q (set Host WithGOOS)", s)
	}
}

func mapUnameGOARCH(s string) (string, error) {
	switch s {
	case "amd64", "x86_64":
		return "amd64", nil
	case "arm64", "aarch64":
		return "arm64", nil
	case "arm", "armv7l":
		return "arm", nil
	case "i386", "i686":
		return "386", nil
	default:
		return "", fmt.Errorf("unsupported uname -m %q (set Host WithGOARCH)", s)
	}
}

func buildGonf(ctx context.Context, goos, goarch string) (string, error) {
	key := goos + "/" + goarch
	gonfBuildMu.Lock()
	defer gonfBuildMu.Unlock()
	if path, ok := gonfBuildCache[key]; ok {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	dir, err := os.MkdirTemp("", "gonf-cross-*")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "gonf")
	if err := GoBuildRunner(ctx, goos, goarch, out, gonfCmdPackage); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	gonfBuildCache[key] = out
	return out, nil
}

// remoteStagingPrefix names the mktemp template for the remote staging
// directory used to sync the gonf binary. mktemp appends random
// characters after the trailing dot, so the resulting path is unpredictable;
// createRemoteStagingDir validates the result still has this prefix.
const remoteStagingPrefix = "/tmp/gonf-sync."

// remoteStagingDirRE matches exactly what a well-behaved `mktemp -d
// /tmp/gonf-sync.XXXXXXXX` can produce: the fixed prefix followed by exactly
// as many characters as there are "X"s in the template, each drawn from
// mktemp's portable substitution alphabet (letters and digits), and nothing
// else (anchored at both ends, so embedded whitespace, newlines, or shell
// metacharacters after a valid-looking prefix are rejected rather than
// silently accepted and later concatenated, unescaped, into further remote
// command strings sent over ssh).
var remoteStagingDirRE = regexp.MustCompile(`^` + regexp.QuoteMeta(remoteStagingPrefix) + `[A-Za-z0-9]{8}$`)

// createRemoteStagingDir creates an unpredictable, exclusively-created
// staging directory on the remote host (mode 0700, owned by the SSH login
// user) via `mktemp -d`. This is a plain command with no shell metacharacters
// (no pipes, &&, subshells, or command substitution), so it runs identically
// under sh, bash, and tcsh login shells (see probePlanVersion for the tcsh
// constraint on FreeBSD).
func createRemoteStagingDir(ctx context.Context, t PushTarget) (string, error) {
	out, err := sshCapture(ctx, t, "mktemp -d "+remoteStagingPrefix+"XXXXXXXX")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	// sshCapture deliberately does not fail on a non-zero remote exit (a
	// missing binary is a normal probe outcome elsewhere), so an empty or
	// unexpected result here must be treated as a hard failure rather than
	// silently proceeding with a bogus staging path.
	if dir == "" {
		return "", fmt.Errorf("mktemp -d produced no output (remote command may have failed)")
	}
	if !strings.HasPrefix(dir, remoteStagingPrefix) {
		return "", fmt.Errorf("mktemp -d returned unexpected path %q", dir)
	}
	// dir is concatenated, unescaped, into further remote command strings
	// (install's src, rm -rf's target) that are sent as a single literal
	// string over ssh for the remote login shell to parse. A prefix check
	// alone would accept a suffix containing whitespace, a newline, or shell
	// metacharacters; require an exact match against mktemp's known output
	// shape instead.
	if !remoteStagingDirRE.MatchString(dir) {
		return "", fmt.Errorf("mktemp -d returned a path with an unexpected format %q", dir)
	}
	return dir, nil
}

// removeRemoteStagingDir best-effort removes the remote staging directory
// created by createRemoteStagingDir. It is called on every EnsureRemoteGonf
// exit path (success or failure, via defer) so a staging dir is never leaked
// under /tmp. The directory is owned by the SSH login user, so the removal
// runs unprivileged; a failure is logged and never masks the caller's error.
func removeRemoteStagingDir(ctx context.Context, t PushTarget, dir string) {
	if err := SSHRunner(ctx, bytes.NewReader(nil), t.sshArgv("rm -rf "+dir)); err != nil {
		logger.Warn("ensure gonf: failed to remove remote staging dir %s: %v", dir, err)
	}
}

func remoteInstallCmd(t PushTarget, src, dst string) (string, error) {
	// install(1) is portable enough on OpenBSD/NetBSD/Linux. Cleanup of src
	// is handled by removeRemoteStagingDir (rm -rf on the whole staging
	// dir), so this stays a single simple command with no "&&" chaining.
	inner := fmt.Sprintf("install -m 755 %s %s", src, dst)
	if t.User == "root" || t.User == "" && strings.HasPrefix(t.Host, "root@") {
		return inner, nil
	}
	// Destination() may be user@host with User empty — check prefix.
	if t.User == "" && strings.Contains(t.Destination(), "@") {
		user := strings.SplitN(t.Destination(), "@", 2)[0]
		if user == "root" {
			return inner, nil
		}
	}
	switch t.privilegeMode() {
	case privilege.None:
		// Login must be able to write dst (unusual); still try.
		return inner, nil
	case privilege.Sudo:
		return "sudo -n " + inner, nil
	case privilege.Doas:
		return "doas " + inner, nil
	default:
		return "", fmt.Errorf("ensure gonf: invalid privilege mode")
	}
}

// sshCaptureExec runs argv and returns its combined stdout, stderr, and exec
// error. It is the only part of sshCapture that touches a real process, so
// tests override it to exercise createRemoteStagingDir / probePlanVersion /
// probeUname (and, transitively, EnsureRemoteGonf's generated argv) without a
// real ssh connection.
var sshCaptureExec = func(ctx context.Context, argv []string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// sshCapture runs a remote command and returns combined stdout (stderr discarded
// into the command string via redirects when callers want quiet probes).
func sshCapture(ctx context.Context, t PushTarget, remoteCmd string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	argv := t.sshArgv(remoteCmd)
	stdout, stderr, err := sshCaptureExec(ctx, argv)
	if err != nil && ctx.Err() != nil {
		return "", fmt.Errorf("%w (ssh killed by context: %v)", ctx.Err(), err)
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 255 {
			return "", fmt.Errorf("ssh to %s: %w (%s)", t.Destination(), err, strings.TrimSpace(stderr))
		}
		// Remote command failed but the session worked (e.g. gonf missing).
	}
	return stdout, nil
}
