package remote

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/snonux/gonf/internal/logger"
)

// The remote staging directory the gonf binary is copied into before it is
// installed: created exclusively with mktemp -d, its path validated before
// it is spliced into further remote commands, and removed afterwards.

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
