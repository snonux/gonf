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
)

// fakeRedactor replaces one synthetic secret; FlushPoint keeps back the
// bytes an occurrence could still start in and never cuts through one.
type fakeRedactor struct{ secret string }

func (f fakeRedactor) Redact(s string) string { return strings.ReplaceAll(s, f.secret, "[redacted]") }

func (f fakeRedactor) FlushPoint(s string) int {
	cut := len(s) - (len(f.secret) - 1)
	if i := strings.LastIndex(s, f.secret); i >= 0 && i < cut && cut < i+len(f.secret) {
		cut = i
	}
	return max(cut, 0)
}

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

// Log lines and Redact both use the installed redactor; without one they
// are unchanged.
func TestSetRedactorAppliesToLogLines(t *testing.T) {
	output, restore := CaptureForTest(LevelInfo)
	t.Cleanup(restore)
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
