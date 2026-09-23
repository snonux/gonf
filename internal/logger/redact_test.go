package logger

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snonux/gonf/secret"
)

// fakeRedactor replaces one synthetic secret; FlushPoint keeps back the
// bytes an occurrence could still start in and never cuts through one. It
// has no self-overlapping-match escape hatch (secret.Values' one and only
// caller for that), so unlike the real Redactor it always just redacts the
// cut prefix directly rather than ever needing to hand back an opaque
// whole-prefix marker.
type fakeRedactor struct{ secret string }

func (f fakeRedactor) Redact(s string) string { return strings.ReplaceAll(s, f.secret, "[redacted]") }

func (f fakeRedactor) FlushPoint(s string) (out string, consumed int) {
	cut := len(s) - (len(f.secret) - 1)
	if i := strings.LastIndex(s, f.secret); i >= 0 && i < cut && cut < i+len(f.secret) {
		cut = i
	}
	cut = max(cut, 0)
	return f.Redact(s[:cut]), cut
}

// MaxPending matches the real redactor's own maxPendingLine-sized default
// closely enough for these tests, which install their own short secrets
// rather than exercising the escape hatch at all.
func (f fakeRedactor) MaxPending() int { return maxPendingLine }

func installFake(t *testing.T, secret string) {
	t.Helper()
	SetRedactor(fakeRedactor{secret: secret})
	t.Cleanup(func() { SetRedactor(nil) })
}

// The writer redacts complete lines, so a secret split across two writes is
// still caught, and Close forwards the unterminated tail.
func TestRedactingWriterRedactsSplitLines(t *testing.T) {
	installFake(t, "fake-secret")
	var out strings.Builder
	w := NewRedactingWriter(&out)
	for _, chunk := range []string{"summary: changed Command[x fake-", "secret]\nnext fake-sec", "ret tail"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := out.String(); got != "summary: changed Command[x [redacted]]\n" {
		t.Fatalf("before Close = %q", got)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "fake-secret") || !strings.HasSuffix(got, "next [redacted] tail") {
		t.Fatalf("after Close = %q", got)
	}
}

// An overlong unterminated run is forwarded only up to the redactor's
// FlushPoint, so a secret straddling the forced flush is still redacted.
func TestRedactingWriterForcedFlushKeepsSecretWhole(t *testing.T) {
	installFake(t, "S3cr3tP@ss")
	var out strings.Builder
	w := NewRedactingWriter(&out)
	if _, err := w.Write([]byte(strings.Repeat("x", 65534) + "S3cr")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("3tP@ss\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if got := out.String(); strings.Contains(got, "S3cr") || !strings.HasSuffix(got, "x[redacted]\n") {
		t.Fatalf("forced flush split the secret: ...%q", got[max(len(got)-40, 0):])
	}
}

// A self-overlapping secret (one whose repeat period is shorter than its
// own length, e.g. "x1x1x1x1x1" also matching itself shifted by 2 bytes)
// used to make secret.Values.FlushPoint chase overlapping matches backward
// one at a time until it reached offset 0, so it never advanced: written a
// little at a time with no newline, RedactingWriter's pending buffer grew
// without bound instead of forwarding anything past maxPendingLine. This
// drives the real secret.Values (not the simplified fakeRedactor above)
// through RedactingWriter to confirm the buffer now stays bounded and the
// eventually-forwarded output never shows the raw secret.
func TestRedactingWriterBoundsSelfOverlappingSecret(t *testing.T) {
	var vals secret.Values
	vals.Add([]byte("x1x1x1x1x1"))
	SetRedactor(&vals)
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	const total = 6 * maxPendingLine
	const chunk = 4096
	deadline := time.Now().Add(10 * time.Second)
	for written := 0; written < total; written += chunk {
		if _, err := w.Write([]byte(strings.Repeat("x1", chunk/2))); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("writing stalled after %d bytes; the pending buffer likely never flushes", written)
		}
		w.mu.Lock()
		pending := len(w.pending)
		w.mu.Unlock()
		// A little slack above maxPendingLine for the keep-back tail and
		// the bytes just appended before the next flush check, but nowhere
		// near unbounded growth (the pre-fix behaviour kept every byte
		// ever written).
		if pending > 3*maxPendingLine {
			t.Fatalf("pending buffer grew to %d bytes (> 3x maxPendingLine=%d): still unbounded", pending, maxPendingLine)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "x1x1x1x1x1") {
		t.Fatal("raw secret leaked into forwarded output")
	}
}

// TestRedactingWriterBoundsTwoSecretLeak is the end-to-end regression test
// for task 3d2's confirmed leak: a shorter, periodic secret ("x1x1x1x1x1")
// and a second, longer secret that starts with it. Before the fix, a single
// large Write of the periodic secret's text (no newline yet, driving
// forwardSafePrefix's forced flush past maxPendingLine while the longer
// secret's tail had not arrived) made the escape hatch flush the ENTIRE
// buffer with nothing held back, so the longer secret's tail, written next,
// arrived with its matching prefix already gone and was forwarded raw. This
// reproduces that exact shape through the real RedactingWriter (not just
// secret.Values.FlushPoint directly, which secret.TestValuesFlushPointTwoSecretLeakRegression
// covers) and confirms the fix: the output holds only redaction markers,
// never a fragment of either secret.
func TestRedactingWriterBoundsTwoSecretLeak(t *testing.T) {
	var vals secret.Values
	shorter := "x1x1x1x1x1"
	longer := shorter + strings.Repeat("Q", 30)
	vals.Add([]byte(shorter))
	vals.Add([]byte(longer))
	SetRedactor(&vals)
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	// One big write of pure periodic text, well past maxPendingLine, with
	// no newline: this alone triggers a forced flush inside the Write call,
	// before the longer secret's tail exists anywhere.
	if _, err := w.Write([]byte(strings.Repeat("x1", 40000))); err != nil { // 80000 bytes
		t.Fatal(err)
	}
	// The longer secret's tail, as its own separate write completing the
	// line — exactly what a relayed child process writing in chunks would
	// produce.
	if _, err := w.Write([]byte(strings.Repeat("Q", 30) + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if strings.Contains(got, strings.Repeat("Q", 30)) {
		t.Fatalf("raw tail of the longer secret leaked into forwarded output: %q", got)
	}
	if strings.Contains(got, shorter) {
		t.Fatalf("raw shorter secret leaked into forwarded output: %q", got)
	}
	if !strings.Contains(got, secret.Redacted) {
		t.Fatalf("output was not redacted at all: %q", got)
	}
}

// Log lines and Redact both use the installed redactor; without one they
// are unchanged.
func TestSetRedactorAppliesToLogLines(t *testing.T) {
	var buf strings.Builder
	t.Cleanup(RedirectUnprefixed(&buf, LevelInfo))
	output := buf.String
	if got := Redact("fake-secret"); got != "fake-secret" {
		t.Fatalf("Redact without a redactor = %q", got)
	}
	installFake(t, "fake-secret")
	Info("value %s", "fake-secret")
	if strings.Contains(output(), "fake-secret") || !strings.Contains(output(), "value [redacted]") {
		t.Fatalf("log = %q", output())
	}
}

// RunRelayed bounds the wait for a relay pipe that an orphaned descendant
// still holds: killed by its context, the child returns within the timeout
// plus RelayWaitDelay although the sleeper it forked keeps stdout open, and
// the sleeper is not killed by SIGPIPE — its later write still succeeds.
func TestRunRelayedDoesNotWaitForPipeHolders(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "late-write-ok")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	script := "(sleep 3; echo late && touch " + marker + ") & echo started; sleep 10"
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	out := &syncBuilder{}
	start := time.Now()
	err := RunRelayed(cmd, out)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want the context kill error")
	}
	if limit := 200*time.Millisecond + RelayWaitDelay + time.Second; elapsed > limit {
		t.Fatalf("RunRelayed took %v, want at most %v", elapsed, limit)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the orphan's late write failed: it was cut off (SIGPIPE) instead of drained")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "started") {
		t.Fatalf("relayed output = %q", out.String())
	}
}

// A clean exit with a descendant that writes a little later is a success,
// not exec.ErrWaitDelay, and the late output is relayed.
func TestRunRelayedCleanExitWithLateOutput(t *testing.T) {
	setRelayWaitDelay(t, 10*time.Second) // far above the 1s late write
	cmd := exec.Command("sh", "-c", "echo hi; (sleep 1; echo late) & exit 0")
	out := &syncBuilder{}
	if err := RunRelayed(cmd, out); err != nil {
		t.Fatalf("RunRelayed = %v, want nil for a clean exit", err)
	}
	if got := out.String(); got != "hi\nlate\n" {
		t.Fatalf("relayed output = %q, want both lines", got)
	}
}

// syncBuilder is a strings.Builder safe for the relay goroutine and the
// test reading it.
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// setRelayWaitDelay overrides RunRelayed's delay for one test.
func setRelayWaitDelay(t *testing.T, d time.Duration) {
	t.Helper()
	old := relayWaitDelay
	relayWaitDelay = d
	t.Cleanup(func() { relayWaitDelay = old })
}

// waitForFile polls until path exists or the deadline passes.
func waitForFile(t *testing.T, path string, within time.Duration) bool {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// With a file as destination, an orphan still holding the pipe after the
// delay is handed to a detached cat: RunRelayed returns right after the
// delay, and the orphan's later write still arrives in the file.
func TestRunRelayedHandsOrphanToFile(t *testing.T) {
	setRelayWaitDelay(t, 100*time.Millisecond)
	dir := t.TempDir()
	marker := filepath.Join(dir, "late-write-ok")
	dst, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	cmd := exec.Command("sh", "-c", "echo hi; (sleep 1; echo late && touch "+marker+") & exit 0")
	start := time.Now()
	if err := RunRelayed(cmd, dst); err != nil {
		t.Fatalf("RunRelayed = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("RunRelayed took %v; it must not wait for the orphan", elapsed)
	}
	if !waitForFile(t, marker, 5*time.Second) {
		t.Fatal("the orphan's late write failed after the hand-off")
	}
	time.Sleep(100 * time.Millisecond) // let cat copy the last line
	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi\nlate\n" {
		t.Fatalf("destination = %q, want both lines", got)
	}
}

// relayHelperEnv makes this test binary act as a short-lived gonf that
// relays an orphan-leaving child and exits (TestRelayHelperProcess).
const relayHelperEnv = "GONF_TEST_RELAY_HELPER_MARKER"

// TestRelayHelperProcess is not a test by itself: run by
// TestRunRelayedOrphanOutlivesGonf with relayHelperEnv set, it relays a
// child whose descendant writes one second later, then exits at once.
func TestRelayHelperProcess(t *testing.T) {
	marker := os.Getenv(relayHelperEnv)
	if marker == "" {
		t.Skip("helper process only")
	}
	relayWaitDelay = 100 * time.Millisecond
	cmd := exec.Command("sh", "-c", "(sleep 1; echo late >&2 && touch "+marker+") & exit 0")
	if err := RunRelayed(cmd, os.Stderr); err != nil {
		t.Fatal(err)
	}
	os.Exit(0) // exit while the orphan still holds the pipe
}

// The orphan outlives gonf itself: the helper process exits right after
// the relay delay, and the orphan's later write still succeeds (no SIGPIPE)
// and reaches the helper's stderr.
func TestRunRelayedOrphanOutlivesGonf(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "late-write-ok")
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	helper := exec.Command(os.Args[0], "-test.run=^TestRelayHelperProcess$")
	helper.Env = append(os.Environ(), relayHelperEnv+"="+marker)
	helper.Stderr = stderr
	if err := helper.Run(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if !waitForFile(t, marker, 5*time.Second) {
		t.Fatal("the orphan was killed (SIGPIPE) once gonf exited")
	}
	time.Sleep(100 * time.Millisecond)
	if got, _ := os.ReadFile(stderr.Name()); !strings.Contains(string(got), "late") {
		t.Fatalf("helper stderr = %q, want the orphan's late line", got)
	}
}
