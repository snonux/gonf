package foostore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/snonux/gonf/secret"
)

// fake is a Provider wired to the fake foostore plus the files it reports
// through.
type fake struct {
	p    *Provider
	log  string // FAKE_LOG
	pids string // FAKE_PIDS
}

// testItems maps the references the tests use.
var testItems = map[secret.Ref]Item{
	"garage/rpc_secret": Field("Infra/garage-rpc", "Password"),
	"nsd/tsig.key":      Attachment("Infra/nsd/tsig.key"),
}

// newFake returns a Provider running the fake foostore in mode; cfg's
// Lookup defaults to testItems and env adds FAKE_* settings.
func newFake(t *testing.T, mode string, cfg Config, env ...string) fake {
	t.Helper()
	if cfg.Lookup == nil {
		lookup, err := Items(testItems)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Lookup = lookup
	}
	cfg.Binary = os.Args[0]
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := fake{p: p, log: filepath.Join(dir, "log"), pids: filepath.Join(dir, "pids")}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	p.prefix = []string{"-test.run=^TestHelperProcess$", "--"}
	// GORACE: a -race child otherwise sleeps 1s at exit.
	p.extraEnv = append([]string{"GONF_FAKE_FOOSTORE=1", "FAKE_MODE=" + mode, "GORACE=atexit_sleep_ms=0",
		"FAKE_LOG=" + f.log, "FAKE_PIDS=" + f.pids, "FAKE_SLEEP=" + sleep}, env...)
	return f
}

// events counts the fake's logged invocations of kind ("probe" or "read").
func (f fake) events(t *testing.T, kind string) int {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), kind+"\n")
}

// valueEnv passes value to the fake's ok/passfd modes.
func valueEnv(value []byte) string {
	return "FAKE_VALUE_B64=" + base64.StdEncoding.EncodeToString(value)
}

// requireNoLeak fails when err (in any format) carries fake output.
func requireNoLeak(t *testing.T, err error) {
	t.Helper()
	for _, s := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(s, "TOPSECRET") || strings.Contains(s, "hunter2") {
			t.Fatalf("error leaks foostore output: %s", s)
		}
	}
}

func TestResolveReturnsExactBytes(t *testing.T) {
	value := []byte("\x00line one\n\xffline two\n\n")
	f := newFake(t, "ok", Config{}, valueEnv(value))
	for _, ref := range []secret.Ref{"garage/rpc_secret", "/garage/rpc_secret", "nsd/tsig.key"} {
		got, err := secret.Resolve(context.Background(), f.p, ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if !bytes.Equal(got, value) {
			t.Fatalf("%s: got %q, want %q", ref, got, value)
		}
	}
	if n := f.events(t, "probe"); n != 1 {
		t.Fatalf("contract probed %d times, want once per provider", n)
	}
}

func TestResolvePassesOnlyLogicalReferencesInArgv(t *testing.T) {
	tests := []struct {
		name string
		ref  secret.Ref
		cfg  Config
		want []string
	}{
		{"entry field", "garage/rpc_secret", Config{},
			[]string{"read", "--backend", "keepass", "--exact", "--raw", "--non-interactive",
				"--timeout", "30s", "--field", "Password", "--", "Infra/garage-rpc"}},
		{"attachment with store and timeout", "nsd/tsig.key",
			Config{KDBXPath: "/srv/gonf.kdbx", Timeout: 90 * time.Second},
			[]string{"read", "--backend", "keepass", "--exact", "--raw", "--non-interactive",
				"--timeout", "1m30s", "--kdbx-path", "/srv/gonf.kdbx", "--", "Infra/nsd/tsig.key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, "ok", tt.cfg, valueEnv([]byte("v")),
				"FAKE_EXPECT="+strings.Join(tt.want, "\x1f"))
			if _, err := secret.Resolve(context.Background(), f.p, tt.ref); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResolveHasNoInteractivePath(t *testing.T) {
	// Inherited interactive overrides must not reach the child; the ok fake
	// fails (exit 98) if they do, or if it has a terminal or stdin data.
	t.Setenv("FOOSTORE_SHELL", "/bin/sh")
	t.Setenv("PIN", "1234")
	t.Setenv("FOOSTORE_READ_PASSPHRASE_FD", "0")
	f := newFake(t, "ok", Config{}, valueEnv([]byte("v")))
	if _, err := secret.Resolve(context.Background(), f.p, "garage/rpc_secret"); err != nil {
		t.Fatal(err)
	}

	// A foostore that falls back to prompting finds neither a terminal nor
	// stdin and fails as locked, never as a value.
	f = newFake(t, "prompt", Config{})
	_, err := secret.Resolve(context.Background(), f.p, "garage/rpc_secret")
	if secret.KindOf(err) != secret.ErrUnavailable {
		t.Fatalf("prompting foostore: got %v, want ErrUnavailable", err)
	}
}

// TestChildEnvNeverNil guards the nil-Env-means-inherit-everything foot-gun
// directly at the unit that builds it: os/exec treats a nil Cmd.Env as
// "inherit the parent's whole environment" but a non-nil empty slice as "no
// environment" (see os/exec's Cmd.Env doc). Before task 2c2's fix,
// childEnv(false) returned nil whenever HOME was unset and there was
// nothing else to append (no passphrase FD, no test seam extraEnv) -- the
// exact production shape, since extraEnv is a package test seam that is
// always empty outside this package's own tests.
func TestChildEnvNeverNil(t *testing.T) {
	if home, ok := os.LookupEnv("HOME"); ok {
		os.Unsetenv("HOME")
		t.Cleanup(func() { os.Setenv("HOME", home) })
	}
	p := &Provider{}
	env := p.childEnv(false)
	if env == nil {
		t.Fatal("childEnv returned nil with nothing to append; exec.Cmd would inherit the whole parent environment")
	}
	if len(env) != 0 {
		t.Fatalf("childEnv with nothing to append = %v, want empty", env)
	}
}

// TestChildEnvExcludesParentEnvironment is the "probe" scenario task 2c2 was
// filed from: with HOME unset and sensitive variables set in the test's own
// environment (as under `env -i` or a systemd unit without User=), the real
// child process must see none of them. It runs the actual env(1) binary
// (rather than the package's fake foostore, whose test-seam extraEnv would
// always keep Cmd.Env non-nil and so never exercise the bug) with the exact
// Cmd the provider builds, and asserts it prints nothing -- not the parent's
// environment.
func TestChildEnvExcludesParentEnvironment(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env(1) binary available")
	}
	if home, ok := os.LookupEnv("HOME"); ok {
		os.Unsetenv("HOME")
		t.Cleanup(func() { os.Setenv("HOME", home) })
	}
	t.Setenv("PIN", "pin-leak")
	t.Setenv("FOOSTORE_SHELL", "1")
	t.Setenv("FOOSTORE_READ_PASSPHRASE_FD", "0")
	t.Setenv("GONF_TOKEN", "s3cr3t")

	p := &Provider{cfg: Config{Binary: envBin}}
	cmd := p.command(context.Background(), nil, false)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("child inherited the parent environment: %q", out.String())
	}
}

func TestResolvePassphraseThroughInheritedPipe(t *testing.T) {
	pass := "correct horse battery staple"
	var handed []byte
	cfg := Config{Passphrase: func(context.Context) ([]byte, error) {
		handed = []byte(pass)
		return handed, nil
	}}
	f := newFake(t, "passfd", cfg, valueEnv([]byte("v")), "FAKE_PASS="+pass,
		"FAKE_EXPECT="+strings.Join([]string{"read", "--backend", "keepass", "--exact", "--raw",
			"--non-interactive", "--timeout", "30s", "--field", "Password", "--", "Infra/garage-rpc"}, "\x1f"))
	got, err := secret.Resolve(context.Background(), f.p, "garage/rpc_secret")
	if err != nil || string(got) != "v" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !bytes.Equal(handed, make([]byte, len(pass))) {
		t.Fatal("passphrase buffer was not overwritten after use")
	}

	failing := Config{Passphrase: func(context.Context) ([]byte, error) { return nil, errors.New("agent down") }}
	f = newFake(t, "passfd", failing)
	_, err = secret.Resolve(context.Background(), f.p, "garage/rpc_secret")
	if secret.KindOf(err) != secret.ErrUnavailable || f.events(t, "read") != 0 {
		t.Fatalf("failing passphrase source: got %v after %d reads", err, f.events(t, "read"))
	}
}

func TestResolveClassifiesFailuresWithoutLeaking(t *testing.T) {
	tests := []struct {
		mode string
		want error
	}{
		{"notfound", secret.ErrNotFound},
		{"usage", secret.ErrInvalid},
		{"ambiguous", secret.ErrInvalid},
		{"locked", secret.ErrUnavailable},
		{"corrupt", secret.ErrUnavailable},
		{"io", secret.ErrUnavailable},
		{"exit1", secret.ErrUnavailable},
		{"garbage", secret.ErrUnavailable}, // unknown exit code with output
		{"signal", secret.ErrUnavailable},
		{"big", secret.ErrInvalid}, // over MaxBytes
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			f := newFake(t, tt.mode, Config{MaxBytes: 64})
			got, err := secret.Resolve(context.Background(), f.p, "garage/rpc_secret")
			if got != nil || secret.KindOf(err) != tt.want {
				t.Fatalf("got %q, %v; want kind %v", got, err, tt.want)
			}
			requireNoLeak(t, err)
			if !strings.Contains(err.Error(), `"Infra/garage-rpc"`) {
				t.Fatalf("error does not name the foostore reference: %v", err)
			}
		})
	}
}

func TestResolveUnmappedIsNotFound(t *testing.T) {
	f := newFake(t, "ok", Config{}, valueEnv([]byte("v")))
	_, err := secret.Resolve(context.Background(), f.p, "no/such")
	if !secret.IsNotFound(err) {
		t.Fatalf("got %v, want not found", err)
	}
	if f.events(t, "probe")+f.events(t, "read") != 0 {
		t.Fatal("an unmapped reference ran foostore")
	}
}

func TestResolveRefusesRefsWithoutCanonicalForm(t *testing.T) {
	// Map every reference, so only the canonical-form check can refuse.
	all := func(secret.Ref) (Item, bool) { return Field("Infra/x", "Password"), true }
	f := newFake(t, "ok", Config{Lookup: all}, valueEnv([]byte("v")))
	for _, ref := range []secret.Ref{"", "/", "..", "../x", "a/../../x"} {
		for _, p := range []secret.Provider{f.p, secret.NewSnapshot(f.p)} {
			_, err := secret.Resolve(context.Background(), p, ref)
			if secret.KindOf(err) != secret.ErrInvalid {
				t.Fatalf("%T %q: got %v, want ErrInvalid (never a suppressible not-found)", p, ref, err)
			}
		}
	}
	if n := f.events(t, "probe") + f.events(t, "read"); n != 0 {
		t.Fatalf("an invalid reference ran foostore %d times", n)
	}
}

func TestResolveDoesNotWaitForUnreadPassphrasePipe(t *testing.T) {
	big := bytes.Repeat([]byte("p"), 1<<20) // far beyond a pipe buffer
	cfg := Config{Passphrase: func(context.Context) ([]byte, error) { return bytes.Clone(big), nil }}
	f := newFake(t, "passhold", cfg)
	t.Cleanup(func() { killPids(f.pids) })
	errc := make(chan error, 1)
	go func() {
		_, err := secret.Resolve(context.Background(), f.p, "garage/rpc_secret")
		errc <- err
	}()
	select {
	case err := <-errc:
		if secret.KindOf(err) != secret.ErrUnavailable {
			t.Fatalf("got %v, want ErrUnavailable (locked)", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Resolve blocked on a passphrase pipe a descendant holds unread")
	}
}

func TestKilledByDeadlineNeedsSignal(t *testing.T) {
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		ctx  context.Context
		code int
		want bool
	}{
		{expired, 0, false}, // exited successfully as the deadline passed
		{expired, 1, false}, // foostore's own timeout exit keeps its result
		{expired, -1, true}, // killed
		{context.Background(), -1, false},
	}
	for _, tt := range tests {
		if got := killedByDeadline(tt.ctx, tt.code); got != tt.want {
			t.Fatalf("killedByDeadline(done=%v, %d) = %v, want %v", tt.ctx.Err() != nil, tt.code, got, tt.want)
		}
	}
}

func TestBoundedBufferClearsOutgrownArrays(t *testing.T) {
	b := &boundedBuffer{max: 4096}
	var outgrown [][]byte
	for i := range 20 {
		if cap(b.buf) > 0 {
			outgrown = append(outgrown, b.buf[:cap(b.buf)])
		}
		_, _ = b.Write(bytes.Repeat([]byte{'s'}, 100+i))
	}
	if b.overflow || b.total != len(b.buf) || !bytes.Equal(b.buf, bytes.Repeat([]byte{'s'}, b.total)) {
		t.Fatalf("buffer holds %d of %d bytes, overflow %v", len(b.buf), b.total, b.overflow)
	}
	for _, old := range outgrown {
		if &old[0] != &b.buf[:cap(b.buf)][0] && bytes.IndexByte(old, 's') >= 0 {
			t.Fatal("an outgrown array still holds output bytes")
		}
	}
	last := b.buf[:cap(b.buf)]
	_, _ = b.Write(make([]byte, 4096))
	if !b.overflow || b.buf != nil || bytes.IndexByte(last, 's') >= 0 {
		t.Fatal("overflow did not discard and clear the buffer")
	}
}

func TestResolveRefusesBinaryWithoutContract(t *testing.T) {
	for _, mode := range []string{"old", "probefail"} {
		t.Run(mode, func(t *testing.T) {
			f := newFake(t, mode, Config{})
			_, err := secret.Resolve(context.Background(), f.p, "garage/rpc_secret")
			if secret.KindOf(err) != secret.ErrUnavailable {
				t.Fatalf("got %v, want ErrUnavailable", err)
			}
			requireNoLeak(t, err)
			if n := f.events(t, "read"); n != 0 {
				t.Fatalf("read ran %d times against a binary without the contract", n)
			}
		})
	}

	p, err := New(Config{Binary: filepath.Join(t.TempDir(), "missing"), Lookup: func(secret.Ref) (Item, bool) {
		return Field("a/b", "Password"), true
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secret.Resolve(context.Background(), p, "x"); secret.KindOf(err) != secret.ErrUnavailable {
		t.Fatalf("missing binary: got %v, want ErrUnavailable", err)
	}
}

// TestResolveConcurrentShortDeadlineDoesNotWaitForHungContractCheck
// reproduces gonf task 6c2: a foostore binary hung on the one-time contract
// check (`read --help`) must not force an unrelated concurrent Resolve call
// to wait out the check's own timeout regardless of that caller's own ctx
// deadline. Before the fix, checkContract held p.mu for the whole check, so
// a concurrent caller blocked on the mutex itself -- which does not know
// about context deadlines -- however long the hung binary took; after the
// fix, every caller shares one in-flight check but waits on it with a
// select against its own ctx.Done(), so a short deadline is still honoured.
func TestResolveConcurrentShortDeadlineDoesNotWaitForHungContractCheck(t *testing.T) {
	f := newFake(t, "probehang", Config{})
	// Long enough that, if the bug were present, the short-deadline caller
	// below would clearly still be blocked when this test's own assertion
	// runs; short enough to keep the test itself quick.
	f.p.probeTimeout = 3 * time.Second
	t.Cleanup(func() { killPids(f.pids) })

	// Reference "a" (no deadline of its own): triggers, and waits out, the
	// hung contract check.
	longDone := make(chan error, 1)
	go func() {
		_, err := secret.Resolve(context.Background(), f.p, "garage/rpc_secret")
		longDone <- err
	}()
	waitForFile(f.pids) // the probe's fake grandchild is up: the check is in flight

	// Reference "b" (a short deadline), resolved concurrently with the
	// still-running check that "a" started.
	shortCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, shortErr := secret.Resolve(shortCtx, f.p, "nsd/tsig.key")
	shortElapsed := time.Since(start)

	if !errors.Is(shortErr, context.DeadlineExceeded) {
		t.Fatalf("short-deadline caller: got %v, want an error wrapping context.DeadlineExceeded", shortErr)
	}
	if shortElapsed > time.Second {
		t.Fatalf("short-deadline caller waited %v for an unrelated in-flight contract check (probeTimeout %v): its own 200ms deadline was ignored", shortElapsed, f.p.probeTimeout)
	}

	longErr := <-longDone
	if secret.KindOf(longErr) != secret.ErrUnavailable {
		t.Fatalf("long caller: got %v, want ErrUnavailable once the shared check finally times out", longErr)
	}
	if n := f.events(t, "probe"); n != 1 {
		t.Fatalf("contract probed %d times, want once shared by both concurrent callers", n)
	}
}

func TestResolveTimeoutKillsProcessGroup(t *testing.T) {
	f := newFake(t, "hang", Config{Timeout: 100 * time.Millisecond})
	start := time.Now()
	_, err := secret.Resolve(context.Background(), f.p, "garage/rpc_secret")
	if secret.KindOf(err) != secret.ErrUnavailable {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
	requireNoLeak(t, err)
	if d := time.Since(start); d > 100*time.Millisecond+killGrace+waitDelay+5*time.Second {
		t.Fatalf("timeout took %v", d)
	}
	requireDead(t, f.pids)
}

func TestResolveCancellationKillsProcessGroup(t *testing.T) {
	f := newFake(t, "hang", Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForFile(f.pids)
		cancel()
	}()
	start := time.Now()
	_, err := secret.Resolve(ctx, f.p, "garage/rpc_secret")
	if !errors.Is(err, context.Canceled) || secret.KindOf(err) != nil {
		t.Fatalf("got %v, want an untyped error wrapping context.Canceled", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("cancellation took %v", d)
	}
	requireDead(t, f.pids)

	done, stop := context.WithCancel(context.Background())
	stop()
	if _, err := f.p.Resolve(done, "garage/rpc_secret"); !errors.Is(err, context.Canceled) {
		t.Fatalf("done ctx: got %v", err)
	}
}

func TestSnapshotResolvesEachReferenceOnce(t *testing.T) {
	f := newFake(t, "ok", Config{}, valueEnv([]byte("pinned")))
	snap := secret.NewSnapshot(f.p)
	for _, ref := range []secret.Ref{"garage/rpc_secret", "/garage/rpc_secret", "garage/rpc_secret", "nsd/tsig.key"} {
		if got, err := secret.Resolve(context.Background(), snap, ref); err != nil || string(got) != "pinned" {
			t.Fatalf("%s: got %q, %v", ref, got, err)
		}
	}
	if probes, reads := f.events(t, "probe"), f.events(t, "read"); probes != 1 || reads != 2 {
		t.Fatalf("probes=%d reads=%d, want 1 and 2 (one per reference)", probes, reads)
	}

	nf := newFake(t, "notfound", Config{})
	snap = secret.NewSnapshot(nf.p)
	for range 2 {
		if _, err := secret.Resolve(context.Background(), snap, "garage/rpc_secret"); !secret.IsNotFound(err) {
			t.Fatalf("got %v, want not found", err)
		}
	}
	if reads := nf.events(t, "read"); reads != 1 {
		t.Fatalf("not-found read %d times, want once (a snapshot fact)", reads)
	}
}

func TestItemsAndNewValidate(t *testing.T) {
	bad := []map[secret.Ref]Item{
		{"": Field("a", "Password")},
		{"../x": Field("a", "Password")},
		{"a/b": Field("a", "Password"), "/a/b": Field("c", "Password")},
		{"a": Attachment("")},
	}
	for _, table := range bad {
		if _, err := Items(table); err == nil {
			t.Fatalf("Items(%v) accepted", table)
		}
	}
	for _, cfg := range []Config{{}, {Lookup: func(secret.Ref) (Item, bool) { return Item{}, false }, Timeout: -1},
		{Lookup: func(secret.Ref) (Item, bool) { return Item{}, false }, MaxBytes: -1}} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("New(%+v) accepted", cfg)
		}
	}
	p, err := New(Config{Lookup: func(secret.Ref) (Item, bool) { return Item{}, true }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secret.Resolve(context.Background(), p, "a"); secret.KindOf(err) != secret.ErrInvalid {
		t.Fatalf("empty foostore reference: got %v, want ErrInvalid", err)
	}
}

// waitForFile polls until path exists (the fake wrote its pids).
func waitForFile(path string) {
	for range 1000 {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// requireDead fails unless every pid in path is gone (or a zombie awaiting
// its reaper), i.e. the whole process group was killed.
func requireDead(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fake never started: %v", err)
	}
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !gone(pid) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("process %d survived", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// killPids kills every pid recorded in path (test cleanup of a fake's
// deliberately orphaned grandchild).
func killPids(path string) {
	data, _ := os.ReadFile(path)
	for _, field := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(field); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// gone reports a pid that no longer runs: absent, or a zombie (Linux
// /proc state Z) that only waits to be reaped by its new parent.
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(stat, ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
