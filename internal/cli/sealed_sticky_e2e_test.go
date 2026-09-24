package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/internal/privilege"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/internal/testutil"
	"github.com/snonux/gonf/plan"
)

// loopbackRemote stands in for SSH: every remote command internal/remote
// sends runs in-process against this package's real "gonf apply" instead,
// with the controller's /tmp sticky dir mapped to a test directory. It
// records every stdin frame and, after each apply session, whether the
// sticky dir held a secret.
type loopbackRemote struct {
	t      *testing.T
	sticky string // local stand-in for the controller's sticky path
	mu     sync.Mutex
	frames [][]byte
	codes  []int
	out    strings.Builder
}

// installLoopbackRemote routes remote.SSHRunner through l until t ends,
// with the bootstrap step observed (no install) and the remote release at
// the sealed-sticky floor.
func installLoopbackRemote(t *testing.T) *loopbackRemote {
	t.Helper()
	l := &loopbackRemote{t: t, sticky: filepath.Join(t.TempDir(), "sticky")}
	oldSSH := remote.SSHRunner
	restoreBootstrap := remote.ObserveBootstrapForTest(func(remote.PushTarget) {})
	restoreRelease := remote.AssumeRemoteSealedStickyForTest()
	t.Cleanup(func() {
		restoreRelease()
		restoreBootstrap()
		remote.SSHRunner = oldSSH
	})
	remote.SSHRunner = l.run
	return l
}

// run executes one remote command: "rm -rf <sticky>" removes the mapped
// sticky dir, and "... apply <args>" runs cliApply with stdin.
func (l *loopbackRemote) run(_ context.Context, stdin io.Reader, argv []string) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if strings.Contains(strings.Join(argv, " "), "AGE-SECRET") {
		l.t.Fatal("the ephemeral key is on a remote command's argv")
	}
	cmd := argv[len(argv)-1]
	if strings.HasPrefix(cmd, "rm -rf ") {
		assertNoStickyPlaintext(l.t, l.sticky)
		return os.RemoveAll(l.sticky)
	}
	args := l.applyArgs(cmd)
	withStdinBytes(l.t, data)
	var code int
	out := testutil.CaptureStderr(l.t, func() { code = cliApply(context.Background(), args) })
	assertNoStickyPlaintext(l.t, l.sticky)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.frames = append(l.frames, data)
	l.codes = append(l.codes, code)
	l.out.WriteString(out)
	return nil
}

// applyArgs returns the arguments after "apply" in cmd, with the sticky
// path mapped to l.sticky and -relayed dropped (it only makes this test
// process ignore SIGPIPE).
func (l *loopbackRemote) applyArgs(cmd string) []string {
	fields := strings.Fields(cmd)
	var args []string
	for i, f := range fields {
		if f == "apply" {
			args = fields[i+1:]
			break
		}
	}
	var out []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-relayed":
		case args[i] == "-apply-dir" && i+1 < len(args):
			out = append(out, "-apply-dir", l.sticky)
			i++
		default:
			out = append(out, args[i])
		}
	}
	if len(out) == 0 {
		l.t.Fatalf("remote command %q has no apply arguments", cmd)
	}
	return out
}

// End to end: the real controller (remote.Delivery.ToHost) pushes a
// two-chunk plan whose elevated chunk writes a sensitive blob through the
// real destination apply. The sticky dir never holds the plaintext after
// any session, the elevated chunk receives the only GONF-PUSH/2 frame and
// applies the secret from its private run dir, which is gone afterwards;
// no output or log carries the key or the secret.
func TestSealedStickyEndToEndPush(t *testing.T) {
	stagingRoot := isolateSealedStagingRoot(t)
	logs := testutil.CaptureLog(t, logger.LevelDebug)
	l := installLoopbackRemote(t)
	root := t.TempDir()
	unpriv, secretOut := filepath.Join(root, "unpriv"), filepath.Join(root, "secret.out")
	mem := plan.NewMemoryStore()
	ref := writeMemBlob(t, mem, "secret.conf", stickySecret)
	ops := []plan.Op{
		{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "e2e"},
		{Op: plan.KindFile, Path: unpriv, Mode: "0600", Payload: plan.FilePayload{ContentB64: "aGVsbG8K"}},
		{Op: plan.KindFile, Path: secretOut, Mode: "0600", Blob: ref, Sensitive: true, Elevate: true},
	}
	d := remote.Delivery{Mode: remote.Push, PlanID: "e2e", Ops: ops, Mem: mem}
	if err := d.ToHost(context.Background(), remote.PushTarget{Host: "h.example", Privilege: privilege.Sudo}); err != nil {
		t.Fatalf("push: %v\n%s", err, l.out.String())
	}
	if len(l.frames) != 3 || l.codes[0] != 0 || l.codes[1] != 0 || l.codes[2] != 0 {
		t.Fatalf("sessions = %d, codes %v; want upload + 2 chunks, all ok:\n%s", len(l.frames), l.codes, l.out.String())
	}
	requireContent(t, secretOut, stickySecret)
	requireContent(t, unpriv, "hello\n")
	for i, frame := range l.frames {
		if keyed := bytes.HasPrefix(frame, []byte("GONF-PUSH/2\n")); keyed != (i == 2) {
			t.Fatalf("session %d keyed=%v; only the elevated chunk (2) may be", i, keyed)
		}
	}
	requireNoLeftoverSealedRunDirs(t, stagingRoot)
	if _, err := os.Stat(l.sticky); !os.IsNotExist(err) {
		t.Fatalf("sticky dir not removed after the push: %v", err)
	}
	key := strings.SplitN(string(l.frames[2]), "\n", 3)[1]
	env := strings.Join(os.Environ(), "\n")
	for _, leak := range []string{strings.TrimPrefix(key, "key "), "AGE-SECRET", stickySecret} {
		if strings.Contains(l.out.String(), leak) || strings.Contains(logs(), leak) || strings.Contains(env, leak) {
			t.Fatalf("output, log or environment leaks %q", leak[:min(len(leak), 12)])
		}
	}
}
