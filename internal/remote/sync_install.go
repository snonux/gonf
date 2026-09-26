package remote

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/shellwords"
	"github.com/snonux/gonf/plan"
)

// Installing a freshly built gonf binary on a remote host: build (or reuse)
// it, copy it into a staging directory (sync_staging.go) with the
// SCPRunner, install(1) it with the target's privilege tool, and verify the
// installed path reports the controller's plan schema.

func (p *Pusher) installRemoteGonf(ctx context.Context, t PushTarget, goos, goarch string) (string, error) {
	localBin, err := p.buildGonf(ctx, goos, goarch)
	if err != nil {
		return "", fmt.Errorf("ensure gonf: build %s/%s: %w", goos, goarch, err)
	}
	installPath := t.GonfPath
	if installPath == "" {
		installPath = "/usr/local/bin/gonf"
	}
	if err := p.installRemoteBinary(ctx, t, localBin, installPath); err != nil {
		return "", err
	}
	return installPath, nil
}

func (p *Pusher) installRemoteBinary(ctx context.Context, t PushTarget, localBin, installPath string) error {
	// Stage the binary under a remote directory that mktemp creates
	// exclusively (mode 0700, owned by the SSH login user): unlike the old
	// fixed "/tmp/gonf.new.<pid>" path, a local attacker on a shared host
	// cannot pre-create it as a symlink or a world-writable file, and cannot
	// read or swap its contents between the scp and the install step. The
	// directory name's random suffix also means two concurrent pushes to the
	// same host (even from the same controller PID) never collide.
	remoteDir, err := createRemoteStagingDir(ctx, t)
	if err != nil {
		return fmt.Errorf("ensure gonf: mktemp: %w", err)
	}
	// Detached from ctx: a push timed out or cancelled mid-scp must still
	// remove the staging dir (as pushRemoveSticky does for the sticky dir).
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pushRemoveStickyTimeout)
		defer cancel()
		removeRemoteStagingDir(cctx, t, remoteDir)
	}()

	remoteTmp := remoteDir + "/gonf"
	if err := p.SCPRunner(ctx, localBin, t, remoteTmp); err != nil {
		// scpArgv's own errors do not repeat the "scp:" tag added here (it
		// used to, producing a double-prefixed "ensure gonf: scp: scp:
		// ExtraSSH option ..." message); a real scp(1) exec failure still
		// gets tagged exactly once, here.
		return fmt.Errorf("ensure gonf: scp: %w", err)
	}

	installCmd, err := remoteInstallCmd(t, remoteTmp, installPath)
	if err != nil {
		return err
	}
	if err := SSHRunner(ctx, bytes.NewReader(nil), t.sshArgv(installCmd)); err != nil {
		return fmt.Errorf("ensure gonf: install: %w", err)
	}

	// Verify via the installed path so PATH order cannot hide an older binary.
	verify := t
	verify.GonfPath = installPath
	got, err := probePlanVersion(ctx, verify, ProbeLogin)
	if err != nil {
		return fmt.Errorf("ensure gonf: verify: %w", err)
	}
	if got < plan.CurrentVersion {
		return fmt.Errorf("ensure gonf: remote still reports plan schema %d after install (want ≥ %d)", got, plan.CurrentVersion)
	}
	logger.Info("push %s: remote gonf plan schema now %d", t.Destination(), got)
	return nil
}

func remoteInstallCmd(t PushTarget, src, dst string) (string, error) {
	// install(1) is portable enough on OpenBSD/NetBSD/Linux. Cleanup of src
	// is handled by removeRemoteStagingDir (rm -rf on the whole staging
	// dir), so this stays a single simple command with no "&&" chaining.
	inner := fmt.Sprintf("install -m 755 %s %s", shellwords.Quote(src), shellwords.Quote(dst))
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
