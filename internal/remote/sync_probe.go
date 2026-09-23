package remote

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/snonux/gonf/internal"
	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
)

// Read-only probes of a remote host for the gonf binary sync: the installed
// gonf's plan-schema, strict-preview and release versions (and comparing
// the release against the controller's), the host's GOOS/GOARCH via uname,
// and sshCapture, the stdout-capturing ssh runner they (and the staging
// directory's mktemp) share.

// ProbeContext names the privilege context a remote version/capability probe
// runs in. It is an explicit argument of every probe (and of the Pusher's
// prober seams) rather than state on PushTarget: a PushTarget describes where
// to connect, while the probe context is an execution mode chosen per call —
// by EnsureRemoteGonf (always the login user) and by strict preview for each
// privilege chunk it will apply (requireRemoteGonfForChunks).
type ProbeContext uint8

const (
	// ProbeLogin runs the probe as the SSH login user, with that user's PATH.
	ProbeLogin ProbeContext = iota
	// ProbeElevated runs the probe through the target's privilege wrapper
	// (sudo/doas), exactly as an elevated apply chunk runs gonf. A sudo/doas
	// secure_path can resolve a different gonf binary than the login PATH,
	// so a login probe alone would not establish the applied binary's
	// capabilities.
	ProbeElevated
)

// sshCaptureExec runs argv and returns its combined stdout, stderr, and exec
// error. It is the only part of sshCapture that touches a real process, so
// tests override it to exercise createRemoteStagingDir / probePlanVersion /
// probeUname (and, transitively, EnsureRemoteGonf's generated argv) without a
// real ssh connection.
var sshCaptureExec = defaultSSHCaptureExec

// probePlanVersion returns the remote binary's plan wire-schema version (as
// reported by "gonf -plan-version"), or 0 with a nil error when the binary
// is missing entirely (empty stdout — a normal, expected probe outcome that
// EnsureRemoteGonf treats as "needs install").
//
// A NON-EMPTY line that fails to parse as an integer is a different
// situation and must not be confused with the missing-binary case: it means
// something is contaminating the ssh session's stdout — a login banner, an
// MOTD, a shell rc file that prints on non-interactive sessions, etc. The
// old behavior silently treated that as "schema 0", which (a) triggered an
// unnecessary rebuild/upgrade cycle and (b) on the post-install verify probe
// (see EnsureRemoteGonf) produced a "remote still reports plan schema 0"
// error that never showed the actual contaminated output, making the real
// cause ("my ssh banner is leaking into stdout") nearly impossible to
// diagnose. Returning an error here instead — one that quotes the raw,
// unparsed line — surfaces that cause directly instead of masking it behind
// a misleading rebuild attempt.
func probePlanVersion(ctx context.Context, t PushTarget, pc ProbeContext) (int, error) {
	cmd, err := remoteProbeCmd(t, pc, "-plan-version")
	if err != nil {
		return 0, err
	}
	// No "2>/dev/null || true": FreeBSD login shells are often tcsh, which
	// mishandles that idiom and yields empty stdout even when gonf works.
	// sshCapture already treats a remote non-zero exit (missing binary) as
	// empty stdout without failing the SSH session.
	out, err := sshCapture(ctx, t, cmd)
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("plan-version probe: %s returned unparseable output %q instead of an integer plan schema version (a login banner, MOTD, or other ssh startup noise may be mixed into the probe output — check the remote login shell's startup files)", cmd, line)
	}
	return n, nil
}

// probeStrictPreviewVersion returns the remote binary's strict-preview
// capability version, or 0 with nil error when the binary does not support
// the probe. RequireRemoteGonf turns that absence into a clear strict-preview
// refusal; ordinary pushes do not need this capability.
func probeStrictPreviewVersion(ctx context.Context, t PushTarget, pc ProbeContext) (int, error) {
	cmd, err := remoteProbeCmd(t, pc, "-strict-preview-version")
	if err != nil {
		return 0, err
	}
	out, err := sshCapture(ctx, t, cmd)
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("strict-preview probe: %s returned unparseable output %q instead of an integer capability version", cmd, line)
	}
	return n, nil
}

// probeReleaseVersion returns the remote gonf binary's own release version
// string (as reported by "gonf -version", e.g. "0.12.1"), or "" with a nil
// error when the binary is missing or too old to support -version.
//
// Like probePlanVersion, raw ssh output can carry a login banner, MOTD, or
// shell startup noise ahead of the real answer, so this probe takes the
// LAST non-empty line of the output (lastNonEmptyLine) rather than the raw
// whole string — the remote command's own answer always prints after any
// such noise, never before it. A locally built remote binary can also
// report a release with a trailing build/pre-release tag (e.g.
// "0.16.6-dev"); versionPrefix keeps only that line's leading
// MAJOR[.MINOR[.PATCH]] digits, since the tag does not change which release
// the binary behaves as for capability comparisons. A line that still has
// no recognizable version prefix after both are applied is reported as an
// error quoting the raw line and naming the banner/MOTD hazard, the same
// way probePlanVersion already does.
//
// This probe is no longer just a best-effort supplement: task ne2 made it
// the SOLE compatibility gate for api.PushPayload (RequireRemoteRelayed) —
// there is no plan-schema check layered underneath that path any more (see
// RequireRemoteRelayed's own doc comment) — so an unhardened parse here
// used to refuse a fully capable remote purely over cosmetic ssh noise
// (task lf2). remoteReleaseIsStale (EnsureRemoteGonf's path, which DOES
// still have the plan-schema check as a backstop) keeps its own, separate
// graceful "unparseable → skip this extra check" handling of this probe's
// error return; RequireRemoteGonf and RequireRemoteRelayed both still treat
// an error here as a hard refusal, since neither has that backstop either.
func probeReleaseVersion(ctx context.Context, t PushTarget, pc ProbeContext) (string, error) {
	cmd, err := remoteProbeCmd(t, pc, "-version")
	if err != nil {
		return "", err
	}
	out, err := sshCapture(ctx, t, cmd)
	if err != nil {
		return "", err
	}
	line := lastNonEmptyLine(out)
	if line == "" {
		return "", nil
	}
	version := versionPrefix(line)
	if version == "" {
		return "", fmt.Errorf("release probe: %s returned unparseable output %q instead of a release version (a login banner, MOTD, or other ssh startup noise may be mixed into the probe output — check the remote login shell's startup files)", cmd, line)
	}
	return version, nil
}

// lastNonEmptyLine returns the last non-blank, trimmed line of s, or "" when
// s has no non-blank line. Ssh startup noise (a login banner, MOTD, shell rc
// output) always prints before the remote command's own output, never after
// it, so the last non-empty line is the answer even when earlier lines are
// contaminated.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// releaseVersionPattern matches a WHOLE line shaped like a release version:
// an optional leading "v"/"V" (parseReleaseVersion's own tolerated prefix),
// MAJOR[.MINOR[.PATCH]] digits (captured in group 1, the part
// parseReleaseVersion actually parses), and an optional trailing build or
// pre-release tag such as "-dev" or "+build3" (dropped: it names extra
// metadata about the same release, not a different one, so keeping it would
// only make an otherwise-valid version string fail to parse). The pattern is
// anchored at BOTH ends — task uf2, since an end-anchor-free match (task
// lf2's fix) accepted any line merely STARTING with digits, so ssh/login
// noise such as "3 updates can be applied immediately.", "2026-09-24", or
// "10:42:01 up 3 days" was wrongly parsed as a version ("3", "2026", "10")
// and could fail RequireRemoteRelayed open — precisely the raw failure that
// gate exists to prevent. A full end-anchor also rejects a fourth dotted
// segment (e.g. "0.16.6.1") instead of silently truncating it, matching
// parseReleaseVersion's own documented more-than-three-segments-is-an-error
// rule.
var releaseVersionPattern = regexp.MustCompile(`^([vV]?\d+(?:\.\d+){0,2})(?:[-+][0-9A-Za-z][0-9A-Za-z.]*)?$`)

// versionPrefix returns line's release-version-shaped numeric part (group 1
// of releaseVersionPattern, with any build/pre-release tag stripped), or ""
// when the WHOLE line does not match that shape.
func versionPrefix(line string) string {
	m := releaseVersionPattern.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

// remoteProbeCmd builds a version/capability probe of t's gonf binary in the
// privilege context pc: ProbeElevated wraps it exactly as an elevated apply
// chunk is wrapped (privilege.WrapApplyBinCmd), ProbeLogin runs it bare.
func remoteProbeCmd(t PushTarget, pc ProbeContext, args string) (string, error) {
	return privilege.WrapApplyBinCmd(t.privilegeMode(), pc == ProbeElevated, remoteGonfBin(t), args)
}

// remoteReleaseIsStale reports whether the remote's own release version
// (probed via p.ReleaseVersionProber) is older than the controller's own
// internal.Version, and if so, a human-readable reason string for the
// caller's log message.
//
// Any probe or parse failure returns (false, "") — never an error: this
// check exists to catch a fix that never bumped plan.CurrentVersion (see
// Pusher.ReleaseVersionProber's doc comment), not to add a new way for a
// push to fail outright. A probe/parse problem is still logged as a
// warning, though, so genuine trouble is still diagnosable instead of being
// swallowed entirely — probeReleaseVersion itself now tolerates the routine
// banner/MOTD/dev-suffix noise (task lf2, mirroring probePlanVersion's
// banner handling), so an error reaching here means something odder.
func (p *Pusher) remoteReleaseIsStale(ctx context.Context, t PushTarget) (bool, string) {
	remoteRelease, err := p.ReleaseVersionProber(ctx, t, ProbeLogin)
	if err != nil {
		logger.Warn("push %s: could not probe remote gonf release version: %v", t.Destination(), err)
		return false, ""
	}
	if remoteRelease == "" {
		// Missing binary or one predating -version: the plan-schema check
		// above is authoritative for that case (a missing binary always
		// probes as plan schema 0, which already forces an upgrade).
		return false, ""
	}
	remoteV, err := parseReleaseVersion(remoteRelease)
	if err != nil {
		logger.Warn("push %s: remote gonf -version output %q did not parse as a MAJOR.MINOR.PATCH release version (a login banner or MOTD may be contaminating ssh output); skipping the release-version staleness check", t.Destination(), remoteRelease)
		return false, ""
	}
	ctrlV, err := parseReleaseVersion(internal.Version)
	if err != nil {
		logger.Warn("push %s: controller's own internal.Version %q did not parse; skipping the release-version staleness check", t.Destination(), internal.Version)
		return false, ""
	}
	if !releaseVersionLess(remoteV, ctrlV) {
		return false, ""
	}
	return true, fmt.Sprintf("remote gonf release %s is older than controller %s (plan schema unchanged)", remoteRelease, internal.Version)
}

// parseReleaseVersion parses a gonf release version string (the
// internal.Version format printed by "gonf -version", e.g. "0.12.1") into up
// to three numeric MAJOR.MINOR.PATCH components. An optional leading "v"
// (as used by this repo's git tags, e.g. v0.12.1) is tolerated. Anything
// else — empty, non-numeric segments, more than three dotted segments — is
// reported as an error rather than guessed at, since a wrong guess here
// would either mask a real upgrade need or trigger a needless one.
func parseReleaseVersion(s string) ([3]int, error) {
	var v [3]int
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return v, fmt.Errorf("release version %q is not in MAJOR.MINOR.PATCH form", s)
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return v, fmt.Errorf("release version %q has a non-numeric segment %q", s, part)
		}
		v[i] = n
	}
	return v, nil
}

// releaseVersionLess reports whether a < b, comparing MAJOR then MINOR then
// PATCH.
func releaseVersionLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
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

// defaultSSHCaptureExec is sshCaptureExec's production implementation. Inside
// a test binary it refuses to exec a real ssh (see refuseNetworkExecInTests):
// sshCapture turns most exec failures into "empty stdout", and the
// release-version probe only logs its errors, so an un-faked probe would
// otherwise pass silently while reaching for the network.
func defaultSSHCaptureExec(ctx context.Context, argv []string) (stdout, stderr string, err error) {
	refuseNetworkExecInTests(argv)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// sshCapture runs a remote command and returns its stdout (stderr discarded
// into the command string via redirects when callers want quiet probes).
// Most probes only need to tell "an answer" from "no answer"; the
// -cmd-timeout capability probe, which must tell an old gonf's flag
// rejection from a sudo/doas refusal, uses sshCaptureWithStderr instead.
func sshCapture(ctx context.Context, t PushTarget, remoteCmd string) (string, error) {
	stdout, _, err := sshCaptureWithStderr(ctx, t, remoteCmd)
	return stdout, err
}

// sshCaptureWithStderr is sshCapture with the remote command's stderr kept.
// An ssh transport failure (exit 255) and a context kill are errors; a
// remote command that merely failed (missing binary, refused sudo, rejected
// flag) is not, so the caller can classify it from the returned streams.
func sshCaptureWithStderr(ctx context.Context, t PushTarget, remoteCmd string) (stdout, stderr string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	argv := t.sshArgv(remoteCmd)
	stdout, stderr, err = sshCaptureExec(ctx, argv)
	if err != nil && ctx.Err() != nil {
		return "", "", fmt.Errorf("%w (ssh killed by context: %v)", ctx.Err(), err)
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 255 {
			return "", "", fmt.Errorf("ssh to %s: %w (%s)", t.Destination(), err, strings.TrimSpace(stderr))
		}
		// Remote command failed but the session worked (e.g. gonf missing).
	}
	return stdout, stderr, nil
}
