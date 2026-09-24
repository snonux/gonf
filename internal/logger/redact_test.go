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

// overclaimRedactor's FlushPoint always reports consuming far more bytes
// than it was ever given, simulating a buggy or malicious third-party
// Redactor implementation. Redactor and SetRedactor are exported API with
// external consumers, so RedactingWriter cannot trust FlushPoint's consumed
// to stay in range: task sd2 finding (a) is that an unclamped consumed
// panicked the relay goroutine via an out-of-range slice.
type overclaimRedactor struct{ fakeRedactor }

func (o overclaimRedactor) FlushPoint(s string) (out string, consumed int) {
	return o.Redact(s), len(s) + 1_000_000
}

// shortOverclaimRedactor is overclaimRedactor's worse cousin: it also
// overclaims consumed, but its out covers only a small fraction of what it
// claims to have consumed (instead of, like overclaimRedactor, happening to
// return an out that already covers the whole buffer). forwardSafePrefix
// cannot re-verify a black-box Redactor's own redaction without redoing it
// itself, so it trusts out exactly as returned: the bytes past what out
// actually covers are silently dropped once consumed is clamped, never
// forwarded raw. This pins that deliberate trade-off (silent loss over a
// leak, see forwardSafePrefix's doc).
type shortOverclaimRedactor struct{ fakeRedactor }

func (shortOverclaimRedactor) FlushPoint(s string) (out string, consumed int) {
	return "[redacted]", len(s) + 1_000_000
}

// nonProgressingRedactor's FlushPoint always reports consuming nothing (the
// legitimate "no safe cut yet" answer FlushPoint's doc allows), so
// RedactingWriter must leave everything pending rather than forward or drop
// anything.
type nonProgressingRedactor struct{ fakeRedactor }

func (nonProgressingRedactor) FlushPoint(s string) (out string, consumed int) {
	return "", -1
}

// raceRedactor reproduces task sd2 finding (b)'s exact TOCTOU window
// deterministically: its MaxPending implementation itself calls
// SetRedactor(nil) the first time it runs, simulating a SetRedactor(nil)
// landing between RedactingWriter.Write's threshold check (pendingLimit,
// which calls MaxPending) and its forced-flush decision (forwardSafePrefix)
// -- the same r.mu-guarded critical section, and exactly the ordering a
// test's t.Cleanup(func() { SetRedactor(nil) }) can produce concurrently in
// production code paths that share this writer. Before the fix, Write's two
// independent currentRedactor() reads meant forwardSafePrefix would then see
// the redactor as already nil and forward its whole pending buffer
// unredacted; the fix reads currentRedactor() once, up front, so
// forwardSafePrefix keeps using the same redactor Write already captured.
type raceRedactor struct {
	fakeRedactor
	cleared bool
}

func (r *raceRedactor) MaxPending() int {
	if !r.cleared {
		r.cleared = true
		SetRedactor(nil)
	}
	return r.fakeRedactor.MaxPending()
}

// An overclaiming FlushPoint must not panic RedactingWriter.Write; consumed
// is clamped to what is actually pending, so it is fully (and safely)
// drained instead. This redactor's out happens to already cover the whole
// buffer (o.Redact(s) redacts every occurrence in all of s), so the
// forwarded output must be exactly that fully-redacted text -- proving out
// reached the destination unmodified, not just that pending emptied out.
func TestRedactingWriterClampsOverclaimingFlushPoint(t *testing.T) {
	secretStr := "S3cr3tP@ss"
	SetRedactor(overclaimRedactor{fakeRedactor{secret: secretStr}})
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	payload := strings.Repeat("x", maxPendingLine) + secretStr
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	pending := len(w.pending)
	w.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending = %d bytes after an overclaiming FlushPoint, want 0 (consumed clamped to everything pending)", pending)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if want, got := strings.Repeat("x", maxPendingLine)+secret.Redacted, out.String(); got != want {
		t.Fatalf("output = %q, want %q (FlushPoint's own out forwarded verbatim)", got, want)
	}
}

// A FlushPoint that overclaims consumed AND returns an out covering only a
// fraction of it must never leak the uncovered surplus raw: forwardSafePrefix
// forwards out exactly as given and silently drops the rest once consumed is
// clamped (see forwardSafePrefix's doc on this trade-off). The destination
// must therefore receive exactly shortOverclaimRedactor's own out and
// nothing else -- in particular none of the raw "x" padding or the raw
// secret that were pending but never covered by out.
func TestRedactingWriterOverclaimingFlushPointNeverLeaksUncoveredSurplus(t *testing.T) {
	secretStr := "S3cr3tP@ss"
	SetRedactor(shortOverclaimRedactor{fakeRedactor{secret: secretStr}})
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	payload := strings.Repeat("x", maxPendingLine) + secretStr
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "[redacted]" {
		t.Fatalf("output = %q, want exactly %q (nothing beyond FlushPoint's own out may reach the destination)", got, "[redacted]")
	}
}

// A FlushPoint that reports consumed <= 0 (nothing safe to flush yet) is a
// no-op: nothing is forwarded and nothing pending is lost.
func TestRedactingWriterNonPositiveConsumedIsNoOp(t *testing.T) {
	SetRedactor(nonProgressingRedactor{fakeRedactor{secret: "S3cr3tP@ss"}})
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	payload := strings.Repeat("x", maxPendingLine+10)
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("output = %q, want nothing forwarded when FlushPoint reports consumed <= 0", got)
	}
	w.mu.Lock()
	pending := len(w.pending)
	w.mu.Unlock()
	if pending != len(payload) {
		t.Fatalf("pending = %d, want all %d bytes kept back when FlushPoint makes no progress", pending, len(payload))
	}
}

// TestRedactingWriterWriteUsesOneRedactorPerCall proves task sd2 finding
// (b)'s fix: a single Write call never straddles two different redactor
// states. raceRedactor clears the installed redactor (as a test's
// t.Cleanup(func(){ SetRedactor(nil) }) does concurrently in production)
// from inside the very first of Write's redactor reads (MaxPending, via
// pendingLimit) -- the same seam finding (b) identified. Before the fix,
// forwardSafePrefix's own, independent currentRedactor() read would then see
// nil and dump the whole pending buffer -- including the secret -- raw. With
// the fix, Write captured the redactor once before pendingLimit ran, so
// forwardSafePrefix still redacts through it.
func TestRedactingWriterWriteUsesOneRedactorPerCall(t *testing.T) {
	secretStr := "S3cr3tP@ss"
	red := &raceRedactor{fakeRedactor: fakeRedactor{secret: secretStr}}
	SetRedactor(red)
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	// The secret sits well before the forced-flush cut (near the very end),
	// so it is part of what forwardSafePrefix forwards this Write call, not
	// what it holds back -- the leak (or lack of one) is visible without a
	// Close, which runs under a separate, later currentRedactor() read and
	// so is not part of this call's TOCTOU window.
	payload := secretStr + strings.Repeat("x", maxPendingLine)
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if !red.cleared {
		t.Fatal("test setup bug: MaxPending never ran, so the race window this test drives was never exercised")
	}
	got := out.String()
	if strings.Contains(got, secretStr) {
		t.Fatalf("forwardSafePrefix used the just-cleared (nil) redactor instead of Write's single captured read: raw secret leaked: %q", got)
	}
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
// without bound instead of forwarding anything past maxPendingLine. These
// two tests drive the real secret.Values (not the simplified fakeRedactor
// above) through RedactingWriter to confirm that hang is still fixed, under
// the rd2-era contract described below.
//
// TestRedactingWriterSelfOverlappingSecretStallsRatherThanLeak pins the
// rd2-era contract for an UNBROKEN self-overlapping secret ("x1x1x1x1x1",
// period 2): unlike the earlier mb2-era expectation (this test used to
// require the pending buffer to stay near maxPendingLine throughout), an
// endlessly repeating, never-varying stream has no occurrence-safe cut
// anywhere in it at all (see
// secret.TestValuesFlushPointSelfOverlappingAboveBoundStallsRatherThanLeak),
// so RedactingWriter's pending buffer is allowed to grow with the input in
// this one, narrow, adversarial shape: confidentiality (never forwarding a
// raw fragment) now strictly outranks the older, weaker boundedness promise
// for exactly this shape. What must still hold, and is checked here: the
// computation itself never hangs (each Write call returns quickly --
// mb2's original, primary concern was a non-terminating backward scan, not
// merely a buffer proportional to a pathological input that never lets up),
// and no raw secret byte is ever forwarded. See
// TestRedactingWriterSelfOverlappingSecretResolvesOnceChainBreaks for the
// realistic case (any actual line eventually varies or ends) where bounded
// memory still holds.
func TestRedactingWriterSelfOverlappingSecretStallsRatherThanLeak(t *testing.T) {
	var vals secret.Values
	vals.Add([]byte("x1x1x1x1x1"))
	SetRedactor(&vals)
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	// Only 2x maxPendingLine, not 6x: since pending never shrinks in this
	// unresolved-chain shape, every Write past the threshold rescans the
	// whole (growing) buffer from scratch (see protectedCrossing's doc on
	// this cost), so total cost grows roughly with the square of how far
	// past the threshold this runs -- 2x is already enough to prove no
	// per-call hang without paying for that quadratic growth in the test
	// itself, especially under -race's added overhead.
	const total = 2 * maxPendingLine
	const chunk = 4096
	deadline := time.Now().Add(20 * time.Second)
	for written := 0; written < total; written += chunk {
		if _, err := w.Write([]byte(strings.Repeat("x1", chunk/2))); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("writing stalled (wall-clock, not just buffered) after %d bytes: the computation itself must never hang", written)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "x1x1x1x1x1") {
		t.Fatal("raw secret leaked into forwarded output")
	}
}

// TestRedactingWriterSelfOverlappingSecretResolvesOnceChainBreaks proves the
// realistic side of the contract above: once a self-overlapping chain
// exceeding maxPendingLine actually ends, RedactingWriter's pending buffer
// comes back down and stays bounded -- the boundedness task mb2 introduced
// the escape hatch for still holds for every input that is not a perfectly
// repeating, never-varying stream.
func TestRedactingWriterSelfOverlappingSecretResolvesOnceChainBreaks(t *testing.T) {
	var vals secret.Values
	vals.Add([]byte("x1x1x1x1x1"))
	SetRedactor(&vals)
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	// See the sibling test above for why this stays at 2x maxPendingLine
	// rather than 6x (the quadratic rescan cost of an unresolved chain).
	const total = 2 * maxPendingLine
	const chunk = 4096
	deadline := time.Now().Add(20 * time.Second)
	for written := 0; written < total; written += chunk {
		if _, err := w.Write([]byte(strings.Repeat("x1", chunk/2))); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("writing stalled after %d bytes", written)
		}
	}
	// The break: real content never repeats forever. Once it stops
	// matching, the chain resolves and pending must come back down.
	if _, err := w.Write([]byte(strings.Repeat("z", chunk))); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	pending := len(w.pending)
	w.mu.Unlock()
	if pending > 3*maxPendingLine {
		t.Fatalf("pending buffer stayed at %d bytes (> 3x maxPendingLine=%d) after the chain broke: it must resolve", pending, maxPendingLine)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "x1x1x1x1x1") {
		t.Fatal("raw secret leaked into forwarded output")
	}
}

// TestRedactingWriterStallCapForcesProgress is the end-to-end regression
// test for task le2: rd2's leak fix correctly made secret.Values.FlushPoint's
// escape hatch refuse an unsafe cut for a densely, self-overlappingly
// matched chain that never finds a safe boundary (see
// secret.TestValuesFlushPointSelfOverlappingAboveBoundStallsRatherThanLeak),
// but a stall that never ends means RedactingWriter's pending buffer (see
// forwardSafePrefix) never shrinks either, so every later Write rescans the
// whole, still-growing buffer -- reintroducing, for this one input shape,
// the exact unbounded-growth/quadratic-cost bug task mb2 fixed. This
// reproduces the measured real-world shape from task le2's own annotation (a
// credentials file's "====...=" divider line, which strongLines tracks as a
// strong contained form on its own, against an unterminated "="-only
// progress-bar line with no newline -- the annotation measured 2 MiB taking
// 9.99s with zero bytes forwarded pre-fix) chunked exactly like a relayed
// child's output (io.Copy's 32 KiB default buffer). The primary assertion is
// direct and deterministic rather than a timing race: pending must never grow
// past a bound close to secret.flushStallCap (unexported; 4x
// secret.MaxSplitGuard, mirrored here), proving the escape hatch forces a
// real flush instead of buffering without bound; a generous wall-clock
// deadline is kept as a secondary sanity net for a hang the bound alone
// might not catch (e.g. a bug that loops without growing pending).
func TestRedactingWriterStallCapForcesProgress(t *testing.T) {
	var vals secret.Values
	// The divider line is tracked directly as the secret; feeding the same
	// repeating character as the "progress bar" is what makes every
	// position in the stream part of some overlapping occurrence -- the
	// exact shape FlushPoint's escape hatch can never find a safe cut in.
	vals.Add([]byte(strings.Repeat("=", 40)))
	SetRedactor(&vals)
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	const chunk = 32 << 10                 // io.Copy's default buffer size
	const total = 6 * secret.MaxSplitGuard // well past secret.flushStallCap (4x MaxSplitGuard)
	// One chunk of slack beyond secret.flushStallCap's own 4x MaxSplitGuard
	// for whatever a single Write call appends before it is checked.
	const pendingBound = 4*secret.MaxSplitGuard + chunk
	deadline := time.Now().Add(20 * time.Second)
	for written := 0; written < total; written += chunk {
		end := min(chunk, total-written)
		if _, err := w.Write([]byte(strings.Repeat("=", end))); err != nil {
			t.Fatal(err)
		}
		w.mu.Lock()
		pending := len(w.pending)
		w.mu.Unlock()
		if pending > pendingBound {
			t.Fatalf("pending buffer grew to %d bytes (> %d) after %d of %d bytes written: the escape hatch must force progress once secret.flushStallCap is crossed instead of buffering without bound", pending, pendingBound, written+end, total)
		}
		if time.Now().After(deadline) {
			t.Fatalf("writing stalled (wall-clock, not just buffered) after %d of %d bytes", written+end, total)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, secret.Redacted) {
		t.Fatalf("output was not redacted at all: %d bytes", len(got))
	}
	if raw := strings.Repeat("=", 100); strings.Contains(got, raw) {
		t.Fatalf("a long run of raw secret material leaked into forwarded output (%d bytes total)", len(got))
	}
}

// TestRedactingWriterBoundsTwoSecretLeak is the end-to-end regression test
// for task 3d2's confirmed leak: a shorter, periodic secret ("x1x1x1x1x1")
// and a second, longer secret that starts with it. Before the 3d2 fix, a
// single large Write of the periodic secret's text (no newline yet, driving
// forwardSafePrefix's forced flush past maxPendingLine while the longer
// secret's tail had not arrived) made the escape hatch flush the ENTIRE
// buffer with nothing held back, so the longer secret's tail, written next,
// arrived with its matching prefix already gone and was forwarded raw. This
// reproduces that exact shape through the real RedactingWriter (not just
// secret.Values.FlushPoint directly, which secret.TestValuesFlushPointTwoSecretLeakRegression
// covers) and confirms the fix: the output holds only redaction markers,
// never a fragment of either secret.
//
// The write is sized past secret.flushStallCap (262144 bytes), not merely
// past maxPendingLine (65536): task le2's own stall-cap fix reopened this
// exact leak in a new shape (task 1g2) by consuming the ENTIRE buffer with
// no keep-back once the cap forced a flush, but only past the cap -- a
// write between maxPendingLine and flushStallCap never drives that code
// path at all and stayed green throughout le2's regression, which is
// exactly the coverage hole task 1g2 closes here.
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
	// One big write of pure periodic text, well past flushStallCap, with no
	// newline: this alone triggers the escape hatch's capped forced flush
	// inside the Write call, before the longer secret's tail exists
	// anywhere.
	if _, err := w.Write([]byte(strings.Repeat("x1", 150000))); err != nil { // 300000 bytes
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

// TestRedactingWriterBoundsChunkedSingleSecretLeak is the end-to-end
// regression test for task rd2 (the THIRD consecutive regression in
// FlushPoint's escape hatch: mb2 -> 3d2 -> rd2), reproducing the production
// shape: relayPipe copies a relayed child's output through io.Copy, whose
// default buffer is 32 KiB, so a long unterminated line reaches
// RedactingWriter.Write in 32 KiB chunks. A single strong secret repeated
// across such a line forms one merged, touching-span run from offset 0 (no
// periodic self-overlap needed -- see
// secret.TestValuesFlushPointRetainedTailAtEOFRegression, which pins the
// same root cause directly at the secret.Values level: the escape hatch's
// cut was an arbitrary byte offset inside that run, not an occurrence
// boundary, so the retained pending tail began mid-occurrence and Redact
// could never match it, forwarding raw fragments once later chunks and
// Close flushed them). This drives the real secret.Values through the real
// RedactingWriter with the exact 32 KiB chunking a relayed child produces
// and confirms the fix: stripping every "[redacted]" marker out of the
// forwarded output leaves nothing, because the whole line was pure repeated
// secret with no legitimate text of its own -- any leftover byte is a
// leaked fragment.
func TestRedactingWriterBoundsChunkedSingleSecretLeak(t *testing.T) {
	var vals secret.Values
	tok := "api-token-9fK2xQabcdE" // 22 bytes: > maxWordLen(12), so strong regardless of its characters
	vals.Add([]byte(tok))
	SetRedactor(&vals)
	t.Cleanup(func() { SetRedactor(nil) })

	var out strings.Builder
	w := NewRedactingWriter(&out)
	const chunk = 32 << 10                                   // io.Copy's default buffer size
	line := strings.Repeat(tok, 6*maxPendingLine/len(tok)+1) // several times MaxSplitGuard, unterminated
	for off := 0; off < len(line); off += chunk {
		end := min(off+chunk, len(line))
		if _, err := w.Write([]byte(line[off:end])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if stripped := strings.ReplaceAll(got, secret.Redacted, ""); stripped != "" {
		t.Fatalf("raw secret fragment leaked into forwarded output: %q (full output %d bytes)", stripped, len(got))
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
	defer func() { _ = dst.Close() }()
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
	defer func() { _ = stderr.Close() }()
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
