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
	"time"

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
	argv, err := scpArgv(PushTarget{
		Host: "r0.lan.buetow.org", User: "root",
		ExtraSSH: []string{"-p", "22", "-o", "StrictHostKeyChecking=yes"},
	}, "/tmp/gonf", "/tmp/gonf.new")
	if err != nil {
		t.Fatalf("scpArgv: %v", err)
	}
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

// TestSCPArgvExtraSSHTranslation is the table-driven coverage the task asks
// for: ssh and scp assign different meanings to several of the same option
// letters (most notably -l and -p), so scpArgv must translate, pass through,
// or reject each ExtraSSH token rather than forwarding it verbatim.
func TestSCPArgvExtraSSHTranslation(t *testing.T) {
	t.Parallel()
	base := PushTarget{Host: "r0.lan.buetow.org"}

	cases := []struct {
		name     string
		extraSSH []string
		// wantContains: substrings that must all appear in the joined argv
		// (order-insensitive checks for the translated/passed-through form).
		wantContains []string
		// wantAbsent: substrings that must NOT appear (the untranslated ssh
		// form, or a token that should have been dropped/rejected).
		wantAbsent []string
		wantErr    bool
	}{
		{
			name:         "separate login user translated to -o User=",
			extraSSH:     []string{"-l", "paul"},
			wantContains: []string{"-o User=paul"},
			wantAbsent:   []string{"-l paul", "-l"},
		},
		{
			name:         "joined login user translated to -o User=",
			extraSSH:     []string{"-lpaul"},
			wantContains: []string{"-o User=paul"},
			wantAbsent:   []string{"-lpaul"},
		},
		{
			name:         "joined port translated to -P",
			extraSSH:     []string{"-p2222"},
			wantContains: []string{"-P 2222"},
			wantAbsent:   []string{"-p2222", "-p 2222"},
		},
		{
			name:         "separate port translated to -P",
			extraSSH:     []string{"-p", "2222"},
			wantContains: []string{"-P 2222"},
			wantAbsent:   []string{"-p 2222", "-p2222"},
		},
		{
			name:         "-o pass through separate",
			extraSSH:     []string{"-o", "StrictHostKeyChecking=yes"},
			wantContains: []string{"-o StrictHostKeyChecking=yes"},
		},
		{
			name:         "-o pass through joined",
			extraSSH:     []string{"-oStrictHostKeyChecking=yes"},
			wantContains: []string{"-oStrictHostKeyChecking=yes"},
		},
		{
			name:         "-i pass through separate",
			extraSSH:     []string{"-i", "/home/paul/.ssh/id_ed25519"},
			wantContains: []string{"-i /home/paul/.ssh/id_ed25519"},
		},
		{
			name:         "-i pass through joined",
			extraSSH:     []string{"-i/home/paul/.ssh/id_ed25519"},
			wantContains: []string{"-i/home/paul/.ssh/id_ed25519"},
		},
		{
			name:         "-F pass through separate",
			extraSSH:     []string{"-F", "/home/paul/.ssh/config"},
			wantContains: []string{"-F /home/paul/.ssh/config"},
		},
		{
			name:         "-F pass through joined",
			extraSSH:     []string{"-F/home/paul/.ssh/config"},
			wantContains: []string{"-F/home/paul/.ssh/config"},
		},
		{
			name:         "-J pass through separate",
			extraSSH:     []string{"-J", "jump.example.org"},
			wantContains: []string{"-J jump.example.org"},
		},
		{
			name:         "-J pass through joined",
			extraSSH:     []string{"-Jjump.example.org"},
			wantContains: []string{"-Jjump.example.org"},
		},
		{
			name:         "-c pass through separate",
			extraSSH:     []string{"-c", "aes256-gcm@openssh.com"},
			wantContains: []string{"-c aes256-gcm@openssh.com"},
		},
		{
			name:         "-c pass through joined",
			extraSSH:     []string{"-caes256-gcm@openssh.com"},
			wantContains: []string{"-caes256-gcm@openssh.com"},
		},
		{
			name:         "-A pass through (agent forwarding, same meaning under scp)",
			extraSSH:     []string{"-A"},
			wantContains: []string{"-A"},
		},
		// Regression coverage for the joined-form -l/-p/-P detectors
		// misfiring on a SEPARATE-form option VALUE token instead of a flag
		// token: the detectors originally checked only the token's second
		// character (and, for -p/-P, that the remainder parsed as digits),
		// never that the token itself starts with '-'. A value token that
		// happens to share that shape was silently reinterpreted as if it
		// were itself a joined flag, corrupting the preceding flag's value
		// (and, for -l, leaving the preceding flag dangling with none).
		{
			name:         "separate -i value with 'l' as its second char is not corrupted into -o User=",
			extraSSH:     []string{"-i", "/local/id_rsa"},
			wantContains: []string{"-i /local/id_rsa"},
			wantAbsent:   []string{"-o User=", "-o User=ocal/id_rsa"},
		},
		{
			name:         "separate -o value with 'l' as its second char is not corrupted into -o User=",
			extraSSH:     []string{"-o", "ClearAllForwardings=yes"},
			wantContains: []string{"-o ClearAllForwardings=yes"},
			wantAbsent:   []string{"-o User="},
		},
		{
			name:         "separate -o value starting with 'Global' is not corrupted into -o User=",
			extraSSH:     []string{"-o", "GlobalKnownHostsFile=/etc/ssh/ssh_known_hosts"},
			wantContains: []string{"-o GlobalKnownHostsFile=/etc/ssh/ssh_known_hosts"},
			wantAbsent:   []string{"-o User="},
		},
		{
			name:         "separate -J value with 'p' as its second char and a numeric tail is not swallowed as a joined port",
			extraSSH:     []string{"-J", "xp2222"},
			wantContains: []string{"-J xp2222"},
			wantAbsent:   []string{"-P 2222"},
		},
		{
			name:         "separate -c value with 'P' as its second char and a numeric tail is not swallowed as a joined port",
			extraSSH:     []string{"-c", "xP80"},
			wantContains: []string{"-c xP80"},
			wantAbsent:   []string{"-P 80"},
		},
		// Round-3 regression coverage: the a[0]=='-' guard added for the
		// round-2 fix only protects against a value that does NOT itself
		// start with '-'. A value that itself starts with '-' and happens to
		// match the joined -l/-p/-P shape (e.g. "-lweird" as the literal
		// value of a preceding "-i") was still misclassified as a flag,
		// corrupting the argv. The index-based rewrite fixes this
		// structurally: once "-i"/"-o" is seen in separate form, the very
		// next token is unconditionally consumed as its value and never
		// re-examined for its own flag-ness, no matter what it starts with.
		{
			name:         "separate -i value that itself looks like a joined -l flag is forwarded literally, not corrupted into -o User=",
			extraSSH:     []string{"-i", "-lweird"},
			wantContains: []string{"-i -lweird"},
			wantAbsent:   []string{"-o User=weird", "-o User="},
		},
		{
			name:         "separate -o value that itself looks like a joined -p flag is forwarded literally, not reinterpreted as a port",
			extraSSH:     []string{"-o", "-p2222lookalike"},
			wantContains: []string{"-o -p2222lookalike"},
			wantAbsent:   []string{"-P 2222"},
		},
		{name: "-L local port-forward rejected", extraSSH: []string{"-L", "8080:localhost:80"}, wantErr: true},
		{name: "-R remote port-forward rejected", extraSSH: []string{"-R", "8080:localhost:80"}, wantErr: true},
		{name: "-D dynamic port-forward rejected (scp -D means sftp-server path)", extraSSH: []string{"-D", "1080"}, wantErr: true},
		{name: "-W stdio-forward rejected", extraSSH: []string{"-W", "host:22"}, wantErr: true},
		{name: "-e escape-char rejected", extraSSH: []string{"-e", "none"}, wantErr: true},
		{name: "-t force-tty rejected", extraSSH: []string{"-t"}, wantErr: true},
		{name: "-T disable-pty rejected (scp -T means disable filename checks)", extraSSH: []string{"-T"}, wantErr: true},
		{name: "-x disable-X11 rejected", extraSSH: []string{"-x"}, wantErr: true},
		// -S is a letter collision, not a shared meaning: ssh's -S is the
		// ControlPath (multiplexing socket), scp's -S is an alternate
		// program to EXECUTE for the transport. Empirically, "scp -S
		// /nonexistent-program ..." tries to exec that path and fails with
		// a confusing "No such file or directory" rather than a clean
		// rejection, so it must be rejected here rather than forwarded.
		{name: "-S separate rejected (letter collision: ssh ControlPath vs scp -S program-to-exec)", extraSSH: []string{"-S", "/usr/bin/ssh"}, wantErr: true},
		{name: "-S joined rejected (letter collision)", extraSSH: []string{"-S/usr/bin/ssh"}, wantErr: true},
		// -f is scp's own undocumented internal "from" (source) protocol
		// listener flag (not in scp's SYNOPSIS, but still recognized by the
		// binary): forwarding it would make the scp subprocess hang waiting
		// for a protocol handshake that never arrives, exactly like -t.
		{name: "-f rejected (scp internal protocol-listener flag, would hang like -t)", extraSSH: []string{"-f", "foo"}, wantErr: true},
		// Malformed/incomplete -l/-p/-P tokens must not fall through to raw
		// pass-through: any direct API caller can construct these via the
		// exported api.PushTarget.ExtraSSH field, not just gonf's own CLI
		// parser (which happens to always grab a value when one exists).
		{name: "bare trailing -l with no value is rejected", extraSSH: []string{"-l"}, wantErr: true},
		{name: "bare trailing -p with no value is rejected", extraSSH: []string{"-p"}, wantErr: true},
		{name: "bare trailing -P with no value is rejected", extraSSH: []string{"-P"}, wantErr: true},
		// The index-based rewrite consumes t.ExtraSSH[i+1] unconditionally
		// once a separate-form value-taking flag is seen; this must be a
		// clear "missing a value" error, not an index-out-of-range panic,
		// when that flag is the very last ExtraSSH token.
		{name: "bare trailing -i (pass-through, separate form) with no value is rejected, not a panic", extraSSH: []string{"-i"}, wantErr: true},
		{name: "bare trailing -o (pass-through, separate form) with no value is rejected, not a panic", extraSSH: []string{"-o"}, wantErr: true},
		{name: "separate -p with non-numeric value is rejected", extraSSH: []string{"-p", "notaport"}, wantErr: true},
		{name: "separate -P with non-numeric value is rejected", extraSSH: []string{"-P", "notaport"}, wantErr: true},
		{name: "joined -pPORT with non-numeric value is rejected", extraSSH: []string{"-pnotaport"}, wantErr: true},
		{name: "joined -PPORT with non-numeric value is rejected", extraSSH: []string{"-Pnotaport"}, wantErr: true},
		// Any flag letter that is in neither the pass-through nor the
		// rejected list must now be rejected by default (finding 1 showed an
		// unlisted letter cannot be assumed harmless), not silently
		// forwarded to scp.
		{name: "unknown flag -q rejected by default", extraSSH: []string{"-q"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := base
			target.ExtraSSH = tc.extraSSH
			argv, err := scpArgv(target, "/tmp/gonf", "/tmp/gonf.new")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("scpArgv(%v) = %v, want error", tc.extraSSH, argv)
				}
				return
			}
			if err != nil {
				t.Fatalf("scpArgv(%v): unexpected error: %v", tc.extraSSH, err)
			}
			joined := strings.Join(argv, " ")
			for _, want := range tc.wantContains {
				if !strings.Contains(joined, want) {
					t.Fatalf("scpArgv(%v) = %q, want substring %q", tc.extraSSH, joined, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(joined, absent) {
					t.Fatalf("scpArgv(%v) = %q, must not contain %q", tc.extraSSH, joined, absent)
				}
			}
		})
	}
}

// scpArgv's own error strings must not start with an "scp:" tag themselves,
// since EnsureRemoteGonf wraps whatever SCPRunner returns as "ensure gonf:
// scp: %w". A redundant "scp:" prefix inside scpArgv's errors used to
// produce a double-prefixed "ensure gonf: scp: scp: ExtraSSH option ..."
// message.
func TestSCPArgvErrorNotDoublePrefixed(t *testing.T) {
	t.Parallel()
	_, err := scpArgv(PushTarget{Host: "h.example", ExtraSSH: []string{"-t"}}, "/tmp/gonf", "/tmp/gonf.new")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.HasPrefix(err.Error(), "scp:") {
		t.Fatalf("scpArgv error must not itself start with %q: %q", "scp:", err.Error())
	}
	wrapped := fmt.Errorf("ensure gonf: scp: %w", err)
	if strings.Contains(wrapped.Error(), "scp: scp:") {
		t.Fatalf("error is double-prefixed: %q", wrapped.Error())
	}
	const want = "ensure gonf: scp: ExtraSSH option"
	if !strings.HasPrefix(wrapped.Error(), want) {
		t.Fatalf("wrapped error = %q, want prefix %q", wrapped.Error(), want)
	}
}

// removeGonfCrossBuildDirs removes any real gonf-cross-* directories a
// test's fake GoBuildRunner created under os.TempDir(), for every
// goos/goarch pair the test used. Registered via t.Cleanup so a test never
// leaks its own scratch state into the developer's real /tmp. Unlike the
// pre-Pusher version of this helper, it never touches package-level state:
// each test below constructs its own *Pusher (via NewPusher), so there is no
// shared build cache/lock map left to reset.
func removeGonfCrossBuildDirs(t *testing.T, keys ...[2]string) {
	t.Helper()
	for _, k := range keys {
		_ = os.RemoveAll(gonfCrossBuildDir(k[0], k[1]))
	}
}

// TestBuildGonfCache and the tests below construct their own *Pusher
// (NewPusher) and call its buildGonf/GoBuildRunner directly, instead of
// swapping a package-level GoBuildRunner var: this is the concrete
// demonstration that Pusher's injected runners work with no global mutable
// state to race on (mirroring how the l5 task's fake BlobReader proved the
// same kind of decoupling for plan.BlobReader). Each test uses its own
// goos/arch key(s) so their on-disk gonfCrossBuildDir never collides with a
// sibling test's, which is what makes t.Parallel() safe here.

func TestBuildGonfCache(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() { removeGonfCrossBuildDirs(t, [2]string{"gonftest1", "amd64"}) })

	p := NewPusher()
	var builds int
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		builds++
		return os.WriteFile(out, []byte("fake"), 0o755)
	}
	p1, err := p.buildGonf(context.Background(), "gonftest1", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := p.buildGonf(context.Background(), "gonftest1", "amd64")
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

// TestBuildGonfNoLeakedScratchDir is the regression test for the leak half of
// the buildGonf bug: os.MkdirTemp("", "gonf-cross-*") used to hand back a
// fresh, uniquely-named directory on every cache-missed build, and nothing
// ever removed it. After a successful build, only the one canonical, bounded
// baseDir (gonfCrossBuildDir) may exist — its "build-*" scratch subdirectory
// (created fresh per build attempt) must always be gone.
func TestBuildGonfNoLeakedScratchDir(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() { removeGonfCrossBuildDirs(t, [2]string{"gonftest2", "amd64"}) })

	p := NewPusher()
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		return os.WriteFile(out, []byte("fake"), 0o755)
	}

	if _, err := p.buildGonf(context.Background(), "gonftest2", "amd64"); err != nil {
		t.Fatal(err)
	}

	baseDir := gonfCrossBuildDir("gonftest2", "amd64")
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", baseDir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "build-") {
			t.Fatalf("scratch dir %q leaked in %s after a successful build", e.Name(), baseDir)
		}
	}
	if _, err := os.Stat(filepath.Join(baseDir, "gonf")); err != nil {
		t.Fatalf("canonical binary missing after build: %v", err)
	}
}

// TestBuildGonfFailedBuildLeavesNoScratchDir is the same regression, on the
// build-failure path: the old code removed the whole (uniquely-named)
// directory on failure, which happened to work only because that directory
// was never shared with anything else. The scratch dir must still be
// removed even when GoBuildRunner fails, without touching the (possibly
// still valid, from an earlier successful build) canonical baseDir.
func TestBuildGonfFailedBuildLeavesNoScratchDir(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() { removeGonfCrossBuildDirs(t, [2]string{"gonftest3", "arm64"}) })

	p := NewPusher()
	wantErr := fmt.Errorf("boom")
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		return wantErr
	}

	if _, err := p.buildGonf(context.Background(), "gonftest3", "arm64"); err == nil {
		t.Fatal("expected build error")
	}

	baseDir := gonfCrossBuildDir("gonftest3", "arm64")
	entries, err := os.ReadDir(baseDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(%s): %v", baseDir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "build-") {
			t.Fatalf("scratch dir %q leaked in %s after a failed build", e.Name(), baseDir)
		}
	}
}

// TestBuildGonfDifferentTargetsDoNotSerialize is the regression test for the
// serialization half of the bug: buildGonf used to hold ONE global mutex
// across the entire `go build` invocation, so an unrelated platform's build
// sat blocked behind an in-flight one even though they share nothing. This
// starts two builds for different goos/goarch keys on the SAME *Pusher
// (proving the per-key lock, not just separate Pusher instances, is what
// unblocks them) whose fake GoBuildRunner blocks until both are confirmed
// in-flight at once (via a channel handshake) — this can only succeed if the
// two builds actually overlap in time; the old single-global-lock
// implementation would deadlock this test (the second build could never
// start until the first, still-blocked one, released the lock it's waiting
// inside of).
func TestBuildGonfDifferentTargetsDoNotSerialize(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		removeGonfCrossBuildDirs(t, [2]string{"gonftest4a", "amd64"}, [2]string{"gonftest4b", "arm64"})
	})

	p := NewPusher()
	var (
		mu       sync.Mutex
		inFlight = map[string]bool{}
	)
	bothInFlight := make(chan struct{})
	closeOnce := sync.Once{}

	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		key := goos + "/" + goarch
		mu.Lock()
		inFlight[key] = true
		both := len(inFlight) == 2
		mu.Unlock()
		if both {
			closeOnce.Do(func() { close(bothInFlight) })
		}
		select {
		case <-bothInFlight:
		case <-time.After(5 * time.Second):
			return fmt.Errorf("timed out waiting for the other target's build to start (builds serialized?)")
		}
		return os.WriteFile(out, []byte("fake"), 0o755)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = p.buildGonf(context.Background(), "gonftest4a", "amd64")
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = p.buildGonf(context.Background(), "gonftest4b", "arm64")
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
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

// install wires the stub into defaultPusher's overridable fields (this stub
// exercises the package-level EnsureRemoteGonf function, i.e. production
// code's real entry point, which always runs against defaultPusher) plus the
// still-package-level SSHRunner/sshCaptureExec vars, and restores all of them
// (plus the build cache) on test cleanup.
func (s *gonfSyncStub) install(t *testing.T) {
	t.Helper()

	oldProber := defaultPusher.PlanVersionProber
	defaultPusher.PlanVersionProber = func(context.Context, PushTarget) (int, error) { return 0, nil }

	oldBuild := defaultPusher.GoBuildRunner
	defaultPusher.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		return os.WriteFile(out, []byte("fake"), 0o755)
	}

	oldSCP := defaultPusher.SCPRunner
	defaultPusher.SCPRunner = func(ctx context.Context, localPath string, t PushTarget, remotePath string) error {
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
		defaultPusher.PlanVersionProber = oldProber
		defaultPusher.GoBuildRunner = oldBuild
		defaultPusher.SCPRunner = oldSCP
		SSHRunner = oldSSH
		sshCaptureExec = oldCapture
		// gonfSyncTarget always builds for linux/amd64; this stub is the only
		// place still exercising defaultPusher's shared build cache (every
		// other build-related test above uses its own private *Pusher), so
		// clearing it here is enough to keep defaultPusher's cache from
		// leaking into a later test.
		defaultPusher.buildMu.Lock()
		defaultPusher.buildCache = map[string]string{}
		defaultPusher.buildKeyLocks = map[string]*sync.Mutex{}
		defaultPusher.buildMu.Unlock()
		removeGonfCrossBuildDirs(t, [2]string{"linux", "amd64"})
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
		mktempDir: "/tmp/gonf-sync.instfl01",
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

	wantRm := "rm -rf /tmp/gonf-sync.instfl01"
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
	s := &gonfSyncStub{mktempDir: "/tmp/gonf-sync.tcshchk1"}
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

	// A correct prefix followed by garbage must still be rejected: dir is
	// later concatenated, unescaped, into further remote command strings
	// (install's src, rm -rf's target) parsed by the remote login shell, so
	// a prefix-only check would let embedded shell metacharacters or
	// whitespace through.
	garbageSuffixes := []string{
		"/tmp/gonf-sync.abc123\nrm -rf /\ndone", // embedded newline + injected command
		"/tmp/gonf-sync.abc123; rm -rf /",       // embedded semicolon
		"/tmp/gonf-sync.abc123`rm -rf /`",       // embedded backtick command substitution
		"/tmp/gonf-sync.ab",                     // too short
		"/tmp/gonf-sync.abc123456",              // too long
		"/tmp/gonf-sync.abc 123",                // embedded space
		"/tmp/gonf-sync./../../etc",             // path traversal, still prefixed
	}
	for _, s := range garbageSuffixes {
		out := s
		sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
			return out + "\n", "", nil
		}
		if _, err := createRemoteStagingDir(context.Background(), PushTarget{Host: "h.example"}); err == nil {
			t.Fatalf("want an error for right-prefix-plus-garbage output %q", s)
		}
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

func TestParseReleaseVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    [3]int
		wantErr bool
	}{
		{"0.12.1", [3]int{0, 12, 1}, false},
		{"v0.12.1", [3]int{0, 12, 1}, false},
		{"V0.12.1", [3]int{0, 12, 1}, false},
		{"1.2", [3]int{1, 2, 0}, false},
		{"3", [3]int{3, 0, 0}, false},
		{"", [3]int{}, true},
		{"abc", [3]int{}, true},
		{"1.2.3.4", [3]int{}, true},
		{"1.x.3", [3]int{}, true},
	}
	for _, tc := range cases {
		got, err := parseReleaseVersion(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseReleaseVersion(%q) = %v, nil; want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseReleaseVersion(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseReleaseVersion(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestReleaseVersionLess(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b [3]int
		want bool
	}{
		{[3]int{0, 11, 0}, [3]int{0, 12, 1}, true},
		{[3]int{0, 12, 1}, [3]int{0, 12, 1}, false},
		{[3]int{0, 12, 2}, [3]int{0, 12, 1}, false},
		{[3]int{1, 0, 0}, [3]int{0, 99, 99}, false},
	}
	for _, tc := range cases {
		if got := releaseVersionLess(tc.a, tc.b); got != tc.want {
			t.Fatalf("releaseVersionLess(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestEnsureRemoteGonfUpgradesWhenReleaseVersionStale is the regression test
// for this task's core fix (see Pusher.ReleaseVersionProber's doc comment):
// a behavior-only bug fix that never bumps plan.CurrentVersion must still
// reach hosts. The plan-schema probe reports "current" on both the
// pre-check and the post-install verify, which alone would mean "nothing to
// do" — but the remote's own release version (gonf -version) is far older
// than the controller's, and that alone must still trigger a full
// build+scp+install cycle.
func TestEnsureRemoteGonfUpgradesWhenReleaseVersionStale(t *testing.T) {
	oldSSH := SSHRunner
	oldCapture := sshCaptureExec
	t.Cleanup(func() {
		SSHRunner = oldSSH
		sshCaptureExec = oldCapture
	})
	t.Cleanup(func() { removeGonfCrossBuildDirs(t, [2]string{"linux", "amd64"}) })

	SSHRunner = func(ctx context.Context, stdin io.Reader, argv []string) error {
		return nil
	}
	sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
		remoteCmd := argv[len(argv)-1]
		switch {
		case strings.HasPrefix(remoteCmd, "mktemp -d "):
			return "/tmp/gonf-sync.stale001\n", "", nil
		case strings.Contains(remoteCmd, "-plan-version"):
			// The post-install verify probe (real probePlanVersion, called
			// directly by EnsureRemoteGonf) also reports the schema as
			// current: this test's whole point is that the schema check
			// alone would never trigger an upgrade here.
			return strconv.Itoa(plan.CurrentVersion) + "\n", "", nil
		default:
			return "", "", fmt.Errorf("gonfSyncStub: unexpected remote command %q", remoteCmd)
		}
	}

	p := NewPusher()
	p.PlanVersionProber = func(context.Context, PushTarget) (int, error) {
		return plan.CurrentVersion, nil
	}
	p.ReleaseVersionProber = func(context.Context, PushTarget) (string, error) {
		return "0.0.1", nil // far older than any real internal.Version
	}
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		return os.WriteFile(out, []byte("fake"), 0o755)
	}
	var scpCalls int
	p.SCPRunner = func(ctx context.Context, localPath string, pt PushTarget, remotePath string) error {
		scpCalls++
		return nil
	}

	installed, err := p.EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err != nil {
		t.Fatalf("EnsureRemoteGonf: %v", err)
	}
	if installed == "" {
		t.Fatal("installed path is empty; want an upgrade to have run despite the current plan schema")
	}
	if scpCalls != 1 {
		t.Fatalf("scpCalls = %d, want 1 (a stale release version must still trigger an upgrade)", scpCalls)
	}
}

// TestEnsureRemoteGonfUpgradesReleasedV014WithoutStrictPreview verifies the
// ordinary-push recovery path for hosts on the immediately preceding release:
// they share the plan schema but lack this release's strict-preview support.
func TestEnsureRemoteGonfUpgradesReleasedV014WithoutStrictPreview(t *testing.T) {
	oldSSH := SSHRunner
	oldCapture := sshCaptureExec
	t.Cleanup(func() {
		SSHRunner = oldSSH
		sshCaptureExec = oldCapture
	})
	t.Cleanup(func() { removeGonfCrossBuildDirs(t, [2]string{"linux", "amd64"}) })

	SSHRunner = func(context.Context, io.Reader, []string) error { return nil }
	sshCaptureExec = func(_ context.Context, argv []string) (string, string, error) {
		if strings.HasPrefix(argv[len(argv)-1], "mktemp -d ") {
			return "/tmp/gonf-sync.prevw001\n", "", nil
		}
		if strings.Contains(argv[len(argv)-1], "-plan-version") {
			return strconv.Itoa(plan.CurrentVersion) + "\n", "", nil
		}
		return "", "", fmt.Errorf("unexpected remote command %q", argv[len(argv)-1])
	}

	p := NewPusher()
	p.PlanVersionProber = func(context.Context, PushTarget) (int, error) { return plan.CurrentVersion, nil }
	p.ReleaseVersionProber = func(context.Context, PushTarget) (string, error) { return "0.14.0", nil }
	p.GoBuildRunner = func(_ context.Context, _, _, out, _ string) error {
		return os.WriteFile(out, []byte("fake"), 0o755)
	}
	var scpCalls int
	p.SCPRunner = func(context.Context, string, PushTarget, string) error {
		scpCalls++
		return nil
	}

	installed, err := p.EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err != nil {
		t.Fatalf("EnsureRemoteGonf() = %v", err)
	}
	if installed == "" || scpCalls != 1 {
		t.Fatalf("installed=%q scpCalls=%d, want ordinary push to install the strict-preview-capable release", installed, scpCalls)
	}
}

// TestEnsureRemoteGonfSkipsUpgradeWhenReleaseVersionCurrentAndSchemaCurrent
// guards the other side of the same check: when neither the plan schema nor
// the release version is stale, no build/scp should happen at all.
func TestEnsureRemoteGonfSkipsUpgradeWhenReleaseVersionCurrentAndSchemaCurrent(t *testing.T) {
	t.Parallel()
	p := NewPusher()
	p.PlanVersionProber = func(context.Context, PushTarget) (int, error) {
		return plan.CurrentVersion, nil
	}
	p.ReleaseVersionProber = func(context.Context, PushTarget) (string, error) {
		return "999.0.0", nil // never older than the controller
	}
	var built, scped bool
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		built = true
		return nil
	}
	p.SCPRunner = func(ctx context.Context, localPath string, pt PushTarget, remotePath string) error {
		scped = true
		return nil
	}

	installed, err := p.EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err != nil {
		t.Fatalf("EnsureRemoteGonf: %v", err)
	}
	if installed != "" {
		t.Fatalf("installed = %q, want empty (no upgrade needed)", installed)
	}
	if built || scped {
		t.Fatalf("built=%v scped=%v, want neither: schema and release version are both current", built, scped)
	}
}

// TestEnsureRemoteGonfUnparseableReleaseVersionSkipsCheckWithoutFailing
// guards the "additive, best-effort" property of the release-version check:
// banner/MOTD noise on "gonf -version" must not fail the whole push (unlike
// the same noise on "-plan-version", which IS a hard error — see
// TestEnsureRemoteGonfUnparseablePlanVersionProbeFailsWithRawOutput — because
// the plan-schema check is authoritative and cannot safely be skipped, while
// the release-version check is a secondary safety net layered on top of it).
func TestEnsureRemoteGonfUnparseableReleaseVersionSkipsCheckWithoutFailing(t *testing.T) {
	t.Parallel()
	p := NewPusher()
	p.PlanVersionProber = func(context.Context, PushTarget) (int, error) {
		return plan.CurrentVersion, nil
	}
	p.ReleaseVersionProber = func(context.Context, PushTarget) (string, error) {
		return "Welcome to Ubuntu 22.04.1 LTS", nil // banner/MOTD noise, not a version
	}
	var built, scped bool
	p.GoBuildRunner = func(ctx context.Context, goos, goarch, out, pkg string) error {
		built = true
		return nil
	}
	p.SCPRunner = func(ctx context.Context, localPath string, pt PushTarget, remotePath string) error {
		scped = true
		return nil
	}

	installed, err := p.EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err != nil {
		t.Fatalf("EnsureRemoteGonf: %v, want nil (unparseable -version output must not fail the push)", err)
	}
	if installed != "" {
		t.Fatalf("installed = %q, want empty", installed)
	}
	if built || scped {
		t.Fatalf("built=%v scped=%v, want neither: unparseable release-version probe output must be skipped, not treated as stale", built, scped)
	}
}

// TestProbePlanVersionUnparseableOutputReturnsRawTextError is the direct
// unit-level regression test for the unparseable-probe-output bug: a
// non-empty "-plan-version" line that isn't an integer (e.g. a login banner
// or MOTD line mixed into ssh's stdout) must produce an error that quotes
// the raw offending text, never a silent "schema 0".
func TestProbePlanVersionUnparseableOutputReturnsRawTextError(t *testing.T) {
	oldCapture := sshCaptureExec
	t.Cleanup(func() { sshCaptureExec = oldCapture })
	const banner = "*** WARNING: unauthorized access to this system is prohibited ***"
	sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
		return banner + "\n", "", nil
	}

	n, err := probePlanVersion(context.Background(), PushTarget{Host: "h.example"})
	if err == nil {
		t.Fatalf("probePlanVersion returned (%d, nil); want an error for unparseable output", n)
	}
	if !strings.Contains(err.Error(), banner) {
		t.Fatalf("error %q does not include the raw unparsed probe output %q", err.Error(), banner)
	}
}

// TestEnsureRemoteGonfUnparseablePlanVersionProbeFailsWithRawOutput is the
// end-to-end version of the same regression: EnsureRemoteGonf itself (via
// the real, default-wired probePlanVersion) must surface the raw banner
// text in its returned error, and must NOT reinterpret the banner as
// "schema 0" (which used to trigger a pointless rebuild/upgrade and then
// fail with a misleading "still reports plan schema 0" message that hid the
// actual cause).
func TestEnsureRemoteGonfUnparseablePlanVersionProbeFailsWithRawOutput(t *testing.T) {
	oldCapture := sshCaptureExec
	t.Cleanup(func() { sshCaptureExec = oldCapture })
	const banner = "*** WARNING: unauthorized use is prohibited ***"
	sshCaptureExec = func(ctx context.Context, argv []string) (string, string, error) {
		return banner + "\n", "", nil
	}

	p := NewPusher() // real probePlanVersion via the default-wired PlanVersionProber
	_, err := p.EnsureRemoteGonf(context.Background(), gonfSyncTarget())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), banner) {
		t.Fatalf("error %q does not surface the raw banner text", err.Error())
	}
	if strings.Contains(err.Error(), "schema 0") {
		t.Fatalf("error %q must not silently reinterpret banner noise as \"schema 0\"", err.Error())
	}
}
