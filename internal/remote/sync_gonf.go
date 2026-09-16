package remote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	argv := scpArgv(t, localPath, remotePath)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (scp killed by context: %v)", ctx.Err(), err)
	}
	return err
}

// scpArgv builds an scp command line. ExtraSSH is translated: ssh's "-p PORT"
// becomes scp's "-P PORT" (scp's "-p" means preserve mtime).
func scpArgv(t PushTarget, localPath, remotePath string) []string {
	argv := []string{"scp"}
	port := t.Port
	for i := 0; i < len(t.ExtraSSH); i++ {
		a := t.ExtraSSH[i]
		if (a == "-p" || a == "-P") && i+1 < len(t.ExtraSSH) {
			if p, err := strconv.Atoi(t.ExtraSSH[i+1]); err == nil {
				if port == 0 {
					port = p
				}
				i++
				continue
			}
		}
		argv = append(argv, a)
	}
	argv = append(argv, "-o", "ConnectTimeout="+sshConnectTimeout)
	if port > 0 {
		argv = append(argv, "-P", strconv.Itoa(port))
	}
	if t.Identity != "" {
		argv = append(argv, "-i", t.Identity)
	}
	return append(argv, localPath, t.Destination()+":"+remotePath)
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

	remoteTmp := "/tmp/gonf.new." + strconv.Itoa(os.Getpid())
	if err := SCPRunner(ctx, localBin, t, remoteTmp); err != nil {
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

func remoteInstallCmd(t PushTarget, src, dst string) (string, error) {
	// install(1) is portable enough on OpenBSD/NetBSD/Linux.
	inner := fmt.Sprintf("install -m 755 %s %s && rm -f %s", src, dst, src)
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

// sshCapture runs a remote command and returns combined stdout (stderr discarded
// into the command string via redirects when callers want quiet probes).
func sshCapture(ctx context.Context, t PushTarget, remoteCmd string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	argv := t.sshArgv(remoteCmd)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		return "", fmt.Errorf("%w (ssh killed by context: %v)", ctx.Err(), err)
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 255 {
			return "", fmt.Errorf("ssh to %s: %w (%s)", t.Destination(), err, strings.TrimSpace(stderr.String()))
		}
		// Remote command failed but the session worked (e.g. gonf missing).
	}
	return stdout.String(), nil
}
