package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/plan"
)

func TestMapUname(t *testing.T) {
	t.Parallel()
	cases := []struct {
		sys, mach, wantOS, wantArch string
	}{
		{"Linux", "x86_64", "linux", "amd64"},
		{"OpenBSD", "amd64", "openbsd", "amd64"},
		{"NetBSD", "aarch64", "netbsd", "arm64"},
		{"FreeBSD", "arm64", "freebsd", "arm64"},
	}
	for _, tc := range cases {
		goos, err := mapUnameGOOS(tc.sys)
		if err != nil || goos != tc.wantOS {
			t.Fatalf("GOOS(%q)=%q %v, want %q", tc.sys, goos, err, tc.wantOS)
		}
		goarch, err := mapUnameGOARCH(tc.mach)
		if err != nil || goarch != tc.wantArch {
			t.Fatalf("GOARCH(%q)=%q %v, want %q", tc.mach, goarch, err, tc.wantArch)
		}
	}
}

func TestRemoteInstallCmdPrivilege(t *testing.T) {
	t.Parallel()
	if plan.CurrentVersion < 1 {
		t.Fatal("CurrentVersion")
	}
	cmd, err := remoteInstallCmd(PushTarget{User: "root", Privilege: privilege.Sudo}, "/tmp/a", "/usr/local/bin/gonf")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cmd, "sudo") {
		t.Fatalf("root install must not use sudo: %q", cmd)
	}
	cmd, err = remoteInstallCmd(PushTarget{User: "rex", Privilege: privilege.Doas}, "/tmp/a", "/usr/local/bin/gonf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "doas ") {
		t.Fatalf("doas install: %q", cmd)
	}
	cmd, err = remoteInstallCmd(PushTarget{User: "paul", Privilege: privilege.Sudo}, "/tmp/a", "/usr/local/bin/gonf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "sudo -n ") {
		t.Fatalf("sudo install: %q", cmd)
	}
}

func TestSCPArgvTranslatesSSHPort(t *testing.T) {
	t.Parallel()
	argv := scpArgv(PushTarget{
		Host: "r0.lan.buetow.org", User: "root",
		ExtraSSH: []string{"-p", "22", "-o", "StrictHostKeyChecking=yes"},
	}, "/tmp/gonf", "/tmp/gonf.new")
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "scp -p ") || strings.Contains(joined, " -p 22") {
		t.Fatalf("scp must not get ssh -p: %v", argv)
	}
	if !strings.Contains(joined, "-P 22") {
		t.Fatalf("want -P 22 in %v", argv)
	}
	if argv[len(argv)-2] != "/tmp/gonf" || !strings.HasSuffix(argv[len(argv)-1], ":/tmp/gonf.new") {
		t.Fatalf("paths: %v", argv)
	}
}

func TestBuildGonfCache(t *testing.T) {
	old := GoBuildRunner
	t.Cleanup(func() {
		GoBuildRunner = old
		gonfBuildMu.Lock()
		gonfBuildCache = map[string]string{}
		gonfBuildMu.Unlock()
	})

	var builds int
	GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		builds++
		return os.WriteFile(out, []byte("fake"), 0o755)
	}
	p1, err := buildGonf(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := buildGonf(context.Background(), "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1 (cache)", builds)
	}
	if p1 != p2 {
		t.Fatalf("cache paths differ: %q vs %q", p1, p2)
	}
	if filepath.Base(p1) != "gonf" {
		t.Fatalf("path = %q", p1)
	}
}

// tcshUnsafeSubstrings lists shell constructs that a tcsh/csh login shell
// (common on FreeBSD) parses differently from sh, per the constraint
// documented on probePlanVersion. None of the commands EnsureRemoteGonf sends
// over ssh should contain any of these: every command is a single simple
// invocation with plain arguments.
var tcshUnsafeSubstrings = []string{"&&", "||", "|", "$(", "`", "2>/dev/null"}

func assertTcshSafe(t *testing.T, label, cmd string) {
	t.Helper()
	for _, bad := range tcshUnsafeSubstrings {
		if strings.Contains(cmd, bad) {
			t.Fatalf("%s = %q contains tcsh-unsafe construct %q", label, cmd, bad)
		}
	}
}

// gonfSyncStub fakes every side effect EnsureRemoteGonf triggers (build, ssh
// capture probes, scp, ssh exec) so tests can assert on the exact generated
// commands without a real network connection, ssh binary, or Go toolchain
// invocation. This mirrors the existing SCPRunner/SSHRunner/GoBuildRunner
// override pattern used elsewhere in this package (see TestBuildGonfCache),
// extended with an override for sshCaptureExec so the mktemp probe and the
// final -plan-version verify probe are fakeable too.
type gonfSyncStub struct {
	mu sync.Mutex

	mktempDir string // stdout the "mktemp -d ..." probe returns (no trailing newline needed)

	scpErr   error
	scpCalls []string // remote paths passed to SCPRunner, in order

	sshErrFn func(remoteCmd string) error // per-command SSHRunner error; nil = always succeed
	sshCmds  []string                     // remote command strings passed to SSHRunner, in order

	captureCmds []string // remote command strings passed through sshCapture, in order
}

// install wires the stub into the package-level overridable vars and
// restores them (plus the build cache) on test cleanup.
func (s *gonfSyncStub) install(t *testing.T) {
	t.Helper()

	oldProber := PlanVersionProber
	PlanVersionProber = func(context.Context, PushTarget) (int, error) { return 0, nil }

	oldBuild := GoBuildRunner
	GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		return os.WriteFile(out, []byte("fake"), 0o755)
	}

	oldSCP := SCPRunner
	SCPRunner = func(ctx context.Context, localPath string, t PushTarget, remotePath string) error {
		s.mu.Lock()
		s.scpCalls = append(s.scpCalls, remotePath)
		s.mu.Unlock()
		return s.scpErr
	}

	oldSSH := SSHRunner
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		remoteCmd := argv[len(argv)-1]
		s.mu.Lock()
		s.sshCmds = append(s.sshCmds, remoteCmd)
		s.mu.Unlock()
		if s.sshErrFn != nil {
			return s.sshErrFn(remoteCmd)
		}
		return nil
	}

	oldCapture := sshCaptureExec
	sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
		remoteCmd := argv[len(argv)-1]
		s.mu.Lock()
		s.captureCmds = append(s.captureCmds, remoteCmd)
		s.mu.Unlock()
		switch {
		case strings.HasPrefix(remoteCmd, "mktemp -d "):
			if s.mktempDir == "" {
				return "", "", nil // simulates a failed remote mktemp: no stdout
			}
			return s.mktempDir + "\n", "", nil
		case strings.Contains(remoteCmd, "-plan-version"):
			return strconv.Itoa(plan.CurrentVersion) + "\n", "", nil
		default:
			return "", "", fmt.Errorf("gonfSyncStub: unexpected remote command %q", remoteCmd)
		}
	}

	t.Cleanup(func() {
		PlanVersionProber = oldProber
		GoBuildRunner = oldBuild
		SCPRunner = oldSCP
		SSHRunner = oldSSH
		sshCaptureExec = oldCapture
		gonfBuildMu.Lock()
		gonfBuildCache = map[string]string{}
		gonfBuildMu.Unlock()
	})
}

func gonfSyncTarget() PushTarget {
	return PushTarget{
		Host:      "h.example",
		User:      "paul",
		Privilege: privilege.Sudo,
		GOOS:      "linux",
		GOARCH:    "amd64", // set so EnsureRemoteGonf skips the uname probe
	}
}

// The staging path must not be the old fixed, PID-derived
// "/tmp/gonf.new.<pid>" name: it must live under a directory that only
// mktemp -d could have produced (unpredictable, exclusively created).
func TestEnsureRemoteGonfStagingPathIsNotPredictable(t *testing.T) {
	s := &gonfSyncStub{mktempDir: "/tmp/gonf-sync.aB3dEfGh"}
	s.install(t)

	installed, err := EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err != nil {
		t.Fatalf("EnsureRemoteGonf: %v", err)
	}
	if installed != "/usr/local/bin/gonf" {
		t.Fatalf("installed = %q", installed)
	}

	if len(s.scpCalls) != 1 {
		t.Fatalf("scpCalls = %v, want 1 call", s.scpCalls)
	}
	got := s.scpCalls[0]
	if got != "/tmp/gonf-sync.aB3dEfGh/gonf" {
		t.Fatalf("scp remote path = %q, want it under the mktemp dir", got)
	}
	if strings.HasPrefix(got, "/tmp/gonf.new.") {
		t.Fatalf("scp remote path %q still uses the old predictable pattern", got)
	}
	// A fixed-name predecessor would be reproducible purely from the PID.
	// Assert the path is not just "/tmp/gonf.new." + pid for the current
	// process, guarding against a regression that reintroduces PID-only
	// naming under a different prefix.
	if got == "/tmp/gonf.new."+strconv.Itoa(os.Getpid()) {
		t.Fatalf("scp remote path %q is PID-derived", got)
	}

	if len(s.sshCmds) != 2 {
		t.Fatalf("sshCmds = %v, want [install, rm -rf]", s.sshCmds)
	}
	wantInstall := "sudo -n install -m 755 /tmp/gonf-sync.aB3dEfGh/gonf /usr/local/bin/gonf"
	if s.sshCmds[0] != wantInstall {
		t.Fatalf("install cmd = %q, want %q", s.sshCmds[0], wantInstall)
	}
	wantRm := "rm -rf /tmp/gonf-sync.aB3dEfGh"
	if s.sshCmds[1] != wantRm {
		t.Fatalf("cleanup cmd = %q, want %q", s.sshCmds[1], wantRm)
	}
}

// Two invocations from the same controller process (same PID, same host) —
// e.g. concurrent pushes to two inventory entries resolving to the same SSH
// host — must land on distinct staging directories. mktemp's exclusive
// creation with a random suffix is what the old os.Getpid()-derived path
// could never guarantee.
func TestEnsureRemoteGonfConcurrentInvocationsGetDistinctStagingDirs(t *testing.T) {
	dirs := []string{"/tmp/gonf-sync.call0001", "/tmp/gonf-sync.call0002"}
	call := 0
	s := &gonfSyncStub{}
	s.install(t)
	// Override the capture stub installed by s.install to hand out a fresh
	// mktemp dir per invocation, exactly as a real remote mktemp -d would.
	oldCapture := sshCaptureExec
	sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
		remoteCmd := argv[len(argv)-1]
		if strings.HasPrefix(remoteCmd, "mktemp -d ") {
			dir := dirs[call]
			call++
			return dir + "\n", "", nil
		}
		return strconv.Itoa(plan.CurrentVersion) + "\n", "", nil
	}
	t.Cleanup(func() { sshCaptureExec = oldCapture })

	target := gonfSyncTarget()
	if _, err := EnsureRemoteGonf(context.Background(), target); err != nil {
		t.Fatalf("first EnsureRemoteGonf: %v", err)
	}
	if _, err := EnsureRemoteGonf(context.Background(), target); err != nil {
		t.Fatalf("second EnsureRemoteGonf: %v", err)
	}

	if len(s.scpCalls) != 2 {
		t.Fatalf("scpCalls = %v, want 2", s.scpCalls)
	}
	if s.scpCalls[0] == s.scpCalls[1] {
		t.Fatalf("both invocations (same PID, same host) staged to the same path %q", s.scpCalls[0])
	}
}

// A failure during scp must not leak the remote staging directory: the
// install command must never run, and the rm -rf cleanup must still fire.
func TestEnsureRemoteGonfCleansUpStagingDirOnSCPFailure(t *testing.T) {
	s := &gonfSyncStub{
		mktempDir: "/tmp/gonf-sync.scpfail1",
		scpErr:    fmt.Errorf("scp: connection reset"),
	}
	s.install(t)

	_, err := EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err == nil {
		t.Fatal("expected an scp error")
	}

	for _, cmd := range s.sshCmds {
		if strings.Contains(cmd, "install ") {
			t.Fatalf("install must not run after scp failure, got sshCmds=%v", s.sshCmds)
		}
	}
	wantRm := "rm -rf /tmp/gonf-sync.scpfail1"
	if len(s.sshCmds) != 1 || s.sshCmds[0] != wantRm {
		t.Fatalf("sshCmds = %v, want exactly [%q] (cleanup on scp failure)", s.sshCmds, wantRm)
	}
}

// A failure during install must also not leak the remote staging directory:
// the rm -rf cleanup must still fire (via defer) even though the install step
// failed.
func TestEnsureRemoteGonfCleansUpStagingDirOnInstallFailure(t *testing.T) {
	s := &gonfSyncStub{
		mktempDir: "/tmp/gonf-sync.installfail1",
		sshErrFn: func(remoteCmd string) error {
			if strings.HasPrefix(remoteCmd, "sudo -n install ") {
				return fmt.Errorf("install: permission denied")
			}
			return nil
		},
	}
	s.install(t)

	_, err := EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err == nil {
		t.Fatal("expected an install error")
	}

	wantRm := "rm -rf /tmp/gonf-sync.installfail1"
	found := false
	for _, cmd := range s.sshCmds {
		if cmd == wantRm {
			found = true
		}
	}
	if !found {
		t.Fatalf("sshCmds = %v, want cleanup cmd %q present despite install failure", s.sshCmds, wantRm)
	}
}

// A remote mktemp -d that produces no stdout (e.g. an old/missing mktemp, or
// a permission problem under /tmp) must be a hard error, never silently
// proceed with an empty or bogus staging path.
func TestEnsureRemoteGonfMktempFailureIsHardError(t *testing.T) {
	s := &gonfSyncStub{mktempDir: ""}
	s.install(t)

	_, err := EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err == nil {
		t.Fatal("expected an error when mktemp -d produces no output")
	}
	if len(s.scpCalls) != 0 {
		t.Fatalf("scp must not run when mktemp fails, got %v", s.scpCalls)
	}
	if len(s.sshCmds) != 0 {
		t.Fatalf("install/cleanup must not run when mktemp fails, got %v", s.sshCmds)
	}
}

// Every command EnsureRemoteGonf sends over ssh (the mktemp probe, the
// install, the cleanup, and the final -plan-version verify) must be a single
// simple command: no pipes, "&&"/"||" chaining, subshells, or command
// substitution, since a FreeBSD login shell may be tcsh (see the constraint
// documented on probePlanVersion).
func TestEnsureRemoteGonfCommandsAreTcshSafe(t *testing.T) {
	s := &gonfSyncStub{mktempDir: "/tmp/gonf-sync.tcshcheck1"}
	s.install(t)

	if _, err := EnsureRemoteGonf(context.Background(), gonfSyncTarget()); err != nil {
		t.Fatalf("EnsureRemoteGonf: %v", err)
	}

	if len(s.captureCmds) == 0 || len(s.sshCmds) == 0 {
		t.Fatalf("expected both captured and ssh-run commands, got captureCmds=%v sshCmds=%v", s.captureCmds, s.sshCmds)
	}
	for _, cmd := range s.captureCmds {
		assertTcshSafe(t, "sshCapture cmd", cmd)
	}
	for _, cmd := range s.sshCmds {
		assertTcshSafe(t, "SSHRunner cmd", cmd)
	}
}

// createRemoteStagingDir sends exactly "mktemp -d <prefix>XXXXXXXX" — a
// single command with no shell metacharacters — and rejects empty or
// unexpectedly-shaped output rather than trusting it blindly.
func TestCreateRemoteStagingDir(t *testing.T) {
	oldCapture := sshCaptureExec
	t.Cleanup(func() { sshCaptureExec = oldCapture })

	var sawCmd string
	sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
		sawCmd = argv[len(argv)-1]
		return "/tmp/gonf-sync.xyz12345\n", "", nil
	}
	dir, err := createRemoteStagingDir(context.Background(), PushTarget{Host: "h.example"})
	if err != nil {
		t.Fatalf("createRemoteStagingDir: %v", err)
	}
	if dir != "/tmp/gonf-sync.xyz12345" {
		t.Fatalf("dir = %q", dir)
	}
	if sawCmd != "mktemp -d /tmp/gonf-sync.XXXXXXXX" {
		t.Fatalf("remote cmd = %q", sawCmd)
	}
	assertTcshSafe(t, "mktemp cmd", sawCmd)

	sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
		return "", "", nil // simulates a failed remote mktemp: no stdout
	}
	if _, err := createRemoteStagingDir(context.Background(), PushTarget{Host: "h.example"}); err == nil {
		t.Fatal("want an error on empty mktemp output")
	}

	sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
		return "/etc/passwd\n", "", nil // not under the expected prefix
	}
	if _, err := createRemoteStagingDir(context.Background(), PushTarget{Host: "h.example"}); err == nil {
		t.Fatal("want an error when mktemp output has an unexpected prefix")
	}
}

// removeRemoteStagingDir issues a single "rm -rf <dir>" and never panics or
// propagates the ssh error (best-effort cleanup; the caller's real error
// must win).
func TestRemoveRemoteStagingDirIsBestEffort(t *testing.T) {
	oldSSH := SSHRunner
	t.Cleanup(func() { SSHRunner = oldSSH })

	var sawArgv []string
	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		sawArgv = append([]string{}, argv...)
		return fmt.Errorf("connection already closed")
	}
	// Must not panic despite SSHRunner failing.
	removeRemoteStagingDir(context.Background(), PushTarget{Host: "h.example"}, "/tmp/gonf-sync.abc")
	if len(sawArgv) == 0 || sawArgv[len(sawArgv)-1] != "rm -rf /tmp/gonf-sync.abc" {
		t.Fatalf("argv = %v", sawArgv)
	}
}

// remoteInstallCmd no longer chains "&& rm -f <src>": cleanup is now the
// staging directory's rm -rf (removeRemoteStagingDir), so this must stay a
// single simple command.
func TestRemoteInstallCmdHasNoShellChaining(t *testing.T) {
	cmd, err := remoteInstallCmd(PushTarget{User: "paul", Privilege: privilege.Sudo}, "/tmp/gonf-sync.x/gonf", "/usr/local/bin/gonf")
	if err != nil {
		t.Fatal(err)
	}
	assertTcshSafe(t, "install cmd", cmd)
	if strings.Contains(cmd, "rm ") {
		t.Fatalf("install cmd = %q should not itself remove src (staging dir cleanup owns that)", cmd)
	}
}
