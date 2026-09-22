package remote

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
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
func probePlanVersion(ctx context.Context, t PushTarget) (int, error) {
	cmd, err := remoteProbeCmd(t, "-plan-version")
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
func probeStrictPreviewVersion(ctx context.Context, t PushTarget) (int, error) {
	cmd, err := remoteProbeCmd(t, "-strict-preview-version")
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
// error when the binary is missing or too old to support -version. Unlike
// probePlanVersion, an unparseable non-empty result is NOT turned into an
// error here — see remoteReleaseIsStale, which treats it as "skip this
// extra check" rather than failing the whole push, since the release-version
// comparison is a best-effort safety net layered on top of the authoritative
// plan-schema check.
func probeReleaseVersion(ctx context.Context, t PushTarget) (string, error) {
	cmd, err := remoteProbeCmd(t, "-version")
	if err != nil {
		return "", err
	}
	out, err := sshCapture(ctx, t, cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// remoteProbeCmd builds a version/capability probe in the same privilege
// context that will run a strict-preview apply chunk.
func remoteProbeCmd(t PushTarget, args string) (string, error) {
	return privilege.WrapApplyBinCmd(t.privilegeMode(), t.probeElevated, remoteGonfBin(t), args)
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
// warning, though, so genuine banner/MOTD contamination on "-version" (the
// same hazard fixed for "-plan-version" in probePlanVersion) remains
// diagnosable instead of being swallowed entirely.
func (p *Pusher) remoteReleaseIsStale(ctx context.Context, t PushTarget) (bool, string) {
	remoteRelease, err := p.ReleaseVersionProber(ctx, t)
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
