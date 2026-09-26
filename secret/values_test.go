package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTokenValue is synthetic secret material; it never names a real secret.
const fakeTokenValue = "fake-token-<&>\"q\"-0123"

func TestValuesContainsTrackedForms(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("  " + fakeTokenValue + "\r\n"))
	escaped, err := json.Marshal(fakeTokenValue)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"exact with whitespace", "  " + fakeTokenValue + "\r\n", true},
		{"trimmed inside text", "key: " + fakeTokenValue + ";", true},
		{"trailing newline trimmed", "x  " + fakeTokenValue + "y", true},
		{"json escaped", `{"k":` + string(escaped) + `}`, true},
		{"unrelated", "nothing secret here", false},
		{"prefix only", fakeTokenValue[:len(fakeTokenValue)-1], false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := v.Contains([]byte(tc.payload)); got != tc.want {
			t.Errorf("%s: Contains(%q) = %v, want %v", tc.name, tc.payload, got, tc.want)
		}
	}
}

func TestValuesShortFormsMatchOnlyWholePayloads(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("ab\n")) // trimmed form "ab" is below MinContainedLen
	if !v.Contains([]byte("ab")) || !v.Contains([]byte("ab\n")) {
		t.Fatal("a short secret must be recognised as a whole payload")
	}
	if v.Contains([]byte("label ab c")) {
		t.Fatal("a short secret must not be searched inside larger payloads")
	}
	if got := v.Redact("tab ab"); got != "tab ab" {
		t.Fatalf("Redact touched ordinary text: %q", got)
	}
	if got := v.Redact("ab"); got != Redacted {
		t.Fatalf("Redact(whole short secret) = %q, want %q", got, Redacted)
	}
}

// TestValuesShortSecretFormsStayWholePayloadOnly pins that the length rule
// is decided on the trimmed secret, not per form: "123\n" (4 bytes raw) and
// the JSON escaping of `a"b` (4 bytes) must not become substring patterns.
func TestValuesShortSecretFormsStayWholePayloadOnly(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("123\n"))
	v.Add([]byte(`a"b`))
	for _, payload := range []string{"x 123\n y", `{"k":"a\"bc"}`, "port 1234"} {
		if v.Contains([]byte(payload)) {
			t.Errorf("Contains(%q) = true, want false for a short secret inside a payload", payload)
		}
	}
	for _, payload := range []string{"123\n", "123", `a"b`, `a\"b`} {
		if !v.Contains([]byte(payload)) {
			t.Errorf("Contains(%q) = false, want true for a whole-payload match", payload)
		}
	}
	if got := v.Redact("x 123\n y"); got != "x 123\n y" {
		t.Errorf("Redact touched a short secret inside text: %q", got)
	}
}

// Overlapping secrets are redacted as one merged span, leaving no part of
// either visible.
func TestValuesRedactMergesOverlappingSecrets(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("abcdef"))
	v.Add([]byte("defghi"))
	if got, want := v.Redact("xxabcdefghixx"), "xx"+Redacted+"xx"; got != want {
		t.Fatalf("Redact = %q, want %q", got, want)
	}
	if got, want := v.Redact("abcdef-defghi"), Redacted+"-"+Redacted; got != want {
		t.Fatalf("Redact = %q, want %q", got, want)
	}
}

// ContainsStrong reports only secrets of at least MinStrongLen bytes.
func TestValuesContainsStrong(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("paul\n"))
	if !v.Contains([]byte("/home/paul/.bashrc")) || v.ContainsStrong([]byte("/home/paul/.bashrc")) {
		t.Fatal("a 4-byte secret must match but not strongly")
	}
	for _, weak := range []string{"postgres", "backup-user", "Administrator"[:12]} {
		v.Add([]byte(weak))
		if v.ContainsStrong([]byte("/home/" + weak)) {
			t.Fatalf("word-like %q must be weak", weak)
		}
	}
	v.Add([]byte("s3cr3tpw"))
	if !v.ContainsStrong([]byte("/home/s3cr3tpw")) {
		t.Fatal("an 8-byte secret with digits must be strong")
	}
	v.Add([]byte("fake-strong-secret\n"))
	if !v.ContainsStrong([]byte("/srv/fake-strong-secret/x")) {
		t.Fatal("an 18-byte secret must match strongly")
	}
}

// FlushPoint keeps back the bytes the longest form could still start in,
// never cuts through an occurrence, and returns the already-redacted text
// for the bytes it does consume.
func TestValuesFlushPoint(t *testing.T) {
	t.Parallel()
	var v Values
	if out, got := v.FlushPoint("abc"); got != 3 || out != "abc" {
		t.Fatalf("FlushPoint without values = (%q, %d), want (\"abc\", 3)", out, got)
	}
	v.Add([]byte("S3cr3tP@ss")) // 10 bytes: keep 9 back
	s := strings.Repeat("x", 20) + "S3cr"
	if out, got := v.FlushPoint(s); got != len(s)-9 || out != s[:len(s)-9] {
		t.Fatalf("FlushPoint = (%q, %d), want (%q, %d)", out, got, s[:len(s)-9], len(s)-9)
	}
	// A complete occurrence crossing the cut moves the cut to its start.
	s = strings.Repeat("x", 20) + "S3cr3tP@ss" + "yyyy"
	if out, got := v.FlushPoint(s); got != 20 || out != s[:20] {
		t.Fatalf("FlushPoint = (%q, %d), want (%q, 20)", out, got, s[:20])
	}
}

// Every strong line of a multi-line secret is a redact-only form of its own.
func TestValuesTrackMultiLineSecretLines(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("-----BEGIN KEY-----\nAAAAfakeline0123456789\napiVersion: v1\nshort\n-----END KEY-----\n"))
	if got := v.Redact("log: AAAAfakeline0123456789 end"); got != "log: "+Redacted+" end" {
		t.Fatalf("a strong line of a multi-line secret must be redacted on its own: %q", got)
	}
	// Line forms never mark or refuse: shared config-like lines, armour
	// lines and even the key body line on its own are no evidence.
	for _, payload := range []string{"AAAAfakeline0123456789", "apiVersion: v1", "-----BEGIN KEY-----", "a short line"} {
		if v.Contains([]byte(payload)) || v.ContainsStrong([]byte(payload)) {
			t.Errorf("line form %q must be redact-only", payload)
		}
	}
	if got := v.Redact("-----BEGIN KEY-----"); got != "-----BEGIN KEY-----" {
		t.Fatalf("PEM armour must not be a form: %q", got)
	}
}

// The whole-secret forms share the trimmed secret's strength: the
// TrimRight form of a whitespace-padded short secret stays weak.
func TestValuesPaddedShortSecretStaysWeak(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("       ab\n"))
	if v.ContainsStrong([]byte("       ab")) || v.Contains([]byte("x       ab y")) {
		t.Fatal("a padded 2-byte secret must stay a weak whole-payload form")
	}
}

// A form longer than MaxSplitGuard does not make FlushPoint hold back the
// whole buffer.
func TestValuesFlushPointIgnoresHugeForms(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte(strings.Repeat("Z9", MaxSplitGuard))) // 128 KiB secret
	s := strings.Repeat("x", 70<<10)
	if out, got := v.FlushPoint(s); got != len(s) || out != s {
		t.Fatalf("FlushPoint = %d bytes, want %d (huge forms are not split-guarded)", got, len(s))
	}
}

// A secret that is "periodic" (a proper suffix of it equals a proper
// prefix, e.g. "x1x1x1x1x1" also matches itself shifted by 2 bytes) makes
// every occurrence overlap the next, chaining together into one run that
// reaches back to offset 0. Below MaxSplitGuard, FlushPoint keeps buffering
// (0 is the correct, conservative answer — nothing forces a flush yet).
func TestValuesFlushPointSelfOverlappingBelowBound(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("x1x1x1x1x1")) // period 2, own length 10: overlaps itself
	s := strings.Repeat("x1", 2048)
	if out, got := v.FlushPoint(s); got != 0 || out != "" {
		t.Fatalf("FlushPoint = (%q, %d), want (\"\", 0) (still below MaxSplitGuard, correctly conservative)", out, got)
	}
}

// TestValuesFlushPointSelfOverlappingAboveBoundStallsRatherThanLeak pins the
// rd2-era contract for a self-overlapping chain that never breaks anywhere
// in s: FlushPoint must make NO progress (consumed 0) rather than ever
// return a cut that lands inside an occurrence. This is a deliberate,
// documented change from the pre-rd2 contract (this test used to require
// cut > 0 here): for a secret whose repeat period is shorter than its own
// length, EVERY point strictly between the chain's start and its end is
// covered by some occurrence (that is exactly what "self-overlapping"
// means), so as long as the chain has not yet broken anywhere in the
// buffer FlushPoint has been given, there is no occurrence-boundary cut to
// return at all -- returning some other, merely-arbitrary byte offset (the
// pre-rd2 behaviour) is precisely the mid-occurrence leak task rd2 fixed
// (see protectedCrossing's doc on secret/values.go). A stall here is always
// safe: the caller (logger.RedactingWriter) keeps buffering and retries
// FlushPoint once more data crosses MaxSplitGuard again -- see
// TestValuesFlushPointSelfOverlappingResolvesOnceChainBreaks for the
// realistic case (any actual line eventually varies or ends) where that
// retry does make bounded progress, which is what task mb2's original
// boundedness goal actually protects against in practice: a HUNG,
// non-terminating COMPUTATION (the old backward-chasing walk), not merely
// a large buffer while an adversarial, perfectly repeating pattern
// continues without any break whatsoever.
func TestValuesFlushPointSelfOverlappingAboveBoundStallsRatherThanLeak(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("x1x1x1x1x1"))                         // 10 bytes, period 2: self-overlapping
	run := strings.Repeat("x1", (MaxSplitGuard/2)+4096) // > MaxSplitGuard bytes, all one chain
	tail := "x1x1x1"                                    // more of the same pattern: the chain never breaks
	s := run + tail

	out, cut := v.FlushPoint(s)
	if cut != 0 || out != "" {
		t.Fatalf("FlushPoint = (%q, %d), want (\"\", 0): an unbroken self-overlapping chain has no safe cut to return", out, cut)
	}
}

// TestValuesFlushPointStallCapBoundary is the regression test for task le2:
// rd2's leak fix made the case above correctly stall rather than guess at an
// unsafe cut, but with nothing else added, that stall never ends for an
// unbroken self-overlapping chain -- reintroducing, for this exact input
// shape, the unbounded pending-buffer growth (and roughly quadratic rescan
// cost, since matchSpans and protectedSpans rescan the whole, still-growing
// buffer on every call that makes no progress) task mb2 fixed. This pins the
// fix's boundary precisely: one byte short of flushStallCap the escape hatch
// still stalls exactly as before (never guessing at an unsafe cut), but at
// flushStallCap it stops waiting and makes progress.
//
// With only ONE tracked form (10 bytes, period 2, self-overlapping), the
// cap forces progress via longestKeepBackForForcedFlush (see FlushPoint):
// the keep-back cut kMaxCut retains the longest proper prefix of
// "x1x1x1x1x1" that is also a suffix of the buffer, 8 bytes here (parity
// breaks the 9-byte candidate). Those 8 bytes are also the tail of the
// complete occurrence ending the buffer, so cutting there would forward
// them raw once the line ends. Before task tg2 this test pinned exactly
// that cut (sg2's driver exemption accepted splitting the driver);
// resolveForcedFlushCut now retreats to the start of that last complete
// occurrence, 2 bytes further back, so the retained tail is one whole
// occurrence that Redact hides.
func TestValuesFlushPointStallCapBoundary(t *testing.T) {
	t.Parallel()
	var v Values
	form := "x1x1x1x1x1" // 10 bytes, period 2: self-overlapping
	v.Add([]byte(form))

	below := strings.Repeat("x1", flushStallCap/2)[:flushStallCap-1] // one byte short of the cap
	if len(below) != flushStallCap-1 {
		t.Fatalf("test setup: len(below) = %d, want %d", len(below), flushStallCap-1)
	}
	out, cut := v.FlushPoint(below)
	if cut != 0 || out != "" {
		t.Fatalf("FlushPoint(len=%d, one byte short of flushStallCap) = (%q, %d), want (\"\", 0): still below the cap, must keep stalling", len(below), out, cut)
	}

	atCap := strings.Repeat("x1", flushStallCap/2) // exactly the cap
	if len(atCap) != flushStallCap {
		t.Fatalf("test setup: len(atCap) = %d, want %d", len(atCap), flushStallCap)
	}
	out, cut = v.FlushPoint(atCap)
	if wantKeepBack := longestPrefixSuffixOverlap(form, atCap); wantKeepBack != 8 {
		t.Fatalf("test setup: keep-back = %d, want 8", wantKeepBack)
	}
	if wantCut := len(atCap) - len(form); cut != wantCut {
		t.Fatalf("FlushPoint(len=%d, exactly flushStallCap) consumed = %d, want %d (the start of the last complete occurrence of %q)", len(atCap), cut, wantCut, form)
	}
	if out != Redacted {
		t.Fatalf("FlushPoint at the cap = %q, want the single opaque marker %q", out, Redacted)
	}
	if red := v.Redact(atCap[cut:]); red != Redacted {
		t.Fatalf("Redact(retained tail %q) = %q, want %q: a split occurrence leaked", atCap[cut:], red, Redacted)
	}
}

// TestValuesFlushPointSelfOverlappingResolvesOnceChainBreaks proves the
// realistic side of the contract above: once a self-overlapping chain
// exceeding MaxSplitGuard actually ends (any real line eventually does,
// whether by varying content or a newline), FlushPoint makes real,
// occurrence-safe progress on it rather than stalling forever -- the
// boundedness task mb2 introduced the escape hatch for still holds for
// every input that is not a perfectly repeating, never-varying stream.
func TestValuesFlushPointSelfOverlappingResolvesOnceChainBreaks(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("x1x1x1x1x1")) // 10 bytes, period 2: self-overlapping
	run := strings.Repeat("x1", (MaxSplitGuard/2)+4096)
	s := run + strings.Repeat("z", 20) // a real break: "z" cannot extend the chain

	out, cut := v.FlushPoint(s)
	if cut == 0 {
		t.Fatal("FlushPoint stayed 0 once the chain broke: it must resolve and make progress")
	}
	if strings.Contains(out, "x1x1x1x1x1") {
		t.Fatalf("raw secret survived in the flushed output: %q", out)
	}
	if cut <= 0 || cut > len(s) {
		t.Fatalf("cut %d out of range for len(s) = %d", cut, len(s))
	}
}

// TestValuesFlushPointTwoSecretLeakRegression is a permanent regression test
// for the vulnerability task 3d2's review found (a regression against
// e87ca0a~1): a shorter, periodic secret S ("x1x1x1x1x1", period 2) chains
// into one giant self-overlapping merged run, and a second, longer secret
// L = S+tail starts with S's exact bytes. The buggy escape hatch of that era
// returned the merged run's raw end (run[1]) instead of the ordinary
// keep-back cut, so once the buffer crossed MaxSplitGuard the WHOLE
// periodic run — including the bytes that are also L's still-incomplete
// prefix — was flushed with nothing held back, and L's tail, arriving on a
// later write with its matching prefix already gone, was forwarded raw (see
// internal/logger.TestRedactingWriterBoundsTwoSecretLeak for the end-to-end
// version).
//
// The buffer is sized past flushStallCap (300000 > 262144), not merely past
// MaxSplitGuard (65536): below flushStallCap this exact shape is an
// unbroken self-overlapping chain with no safe occurrence-boundary cut
// anywhere in it, so FlushPoint correctly makes no progress at all (see
// TestValuesFlushPointSelfOverlappingAboveBoundStallsRatherThanLeak) — a
// size that never drives the escape hatch's stall-cap branch and so never
// exercised it. Task le2's own stall-cap fix reopened this exact leak in a
// new shape once the buffer crosses flushStallCap: it set cut = len(s),
// consuming the WHOLE buffer with nothing held back, which is exactly
// 3d2's original bug reincarnated one level up (task 1g2 fixes it by
// keeping the ordinary keep-back on the capped path too). This size is
// what actually exercises that path and pins the fix.
func TestValuesFlushPointTwoSecretLeakRegression(t *testing.T) {
	t.Parallel()
	var v Values
	s1 := "x1x1x1x1x1"                 // period 2, 10 bytes
	s2 := s1 + strings.Repeat("Q", 30) // 40 bytes, starts with s1
	v.Add([]byte(s1))
	v.Add([]byte(s2))

	// The buffer at the moment the escape hatch's stall cap forces a flush:
	// past flushStallCap, still pure s1-periodic text (s2's tail has not
	// been written yet) — an unbroken self-overlapping chain, so the
	// escape hatch must stop stalling here and make progress, but without
	// ever sweeping away the whole buffer.
	buf := strings.Repeat("x1", 150000) // 300000 bytes, > flushStallCap

	out, consumed := v.FlushPoint(buf)
	if consumed <= 0 {
		t.Fatalf("FlushPoint = (%q, %d), want consumed > 0: past flushStallCap the escape hatch must stop stalling and make progress", out, consumed)
	}
	if consumed >= len(buf) {
		t.Fatalf("FlushPoint consumed the entire buffer (%d of %d bytes): the ordinary keep-back must survive the stall cap, or a longer secret's prefix swept up in this flush is unrecoverable on the next call (task 1g2)", consumed, len(buf))
	}
	if strings.Contains(out, strings.Repeat("Q", 30)) {
		t.Fatalf("raw secret tail leaked into FlushPoint's own output: %q", out)
	}

	// s2's tail arrives on a later write; the retained pending bytes
	// (buf[consumed:], the surviving keep-back) plus the tail must still
	// let Redact find and hide the complete secret — the whole point of
	// keeping the keep-back through the capped flush.
	pending := buf[consumed:] + strings.Repeat("Q", 30) + "\n"
	redactedTail := v.Redact(pending)
	if strings.Contains(redactedTail, strings.Repeat("Q", 30)) {
		t.Fatalf("raw secret tail leaked: %q", redactedTail)
	}
	if !strings.Contains(redactedTail, Redacted) {
		t.Fatalf("the completed secret was not redacted at all: %q", redactedTail)
	}
}

// TestValuesFlushPointRetainedTailAtEOFRegression is a permanent regression
// test for task rd2, the THIRD consecutive regression in this exact escape
// hatch (mb2 -> 3d2 -> rd2). A single strong secret repeated enough times to
// cross MaxSplitGuard forms one merged run starting at offset 0 --
// mergeSpans merges TOUCHING spans, so plain back-to-back repetition of an
// ordinary, non-periodic secret is enough to reach the escape hatch; no
// self-overlapping (periodic) pattern is needed. The escape hatch's cut
// (the ordinary keep-back, len(s)-longest+1) is an arbitrary byte offset
// inside that run, not an occurrence boundary: for "db-password-42" (14
// bytes) it lands exactly 1 byte into the final occurrence, so the buggy
// code retained "b-password-42" (13 of 14 bytes, missing only the leading
// "d") as the pending tail. Redact can never match a partial occurrence, so
// once that tail was later forwarded (by a forced flush completing the
// line, or by Close at EOF -- see
// TestRedactingWriterBoundsChunkedSingleSecretLeak for the EOF shape
// end-to-end), it went out raw, uncensored. The fix snaps the cut back to
// the start of the last occurrence at or before it, so the retained tail is
// always the complete secret.
func TestValuesFlushPointRetainedTailAtEOFRegression(t *testing.T) {
	t.Parallel()
	var v Values
	secretVal := "db-password-42" // 14 bytes; > maxWordLen(12) so strong regardless of its characters
	v.Add([]byte(secretVal))

	// Comfortably above MaxSplitGuard once repeated; strings.Repeat produces
	// an exact multiple of len(secretVal), so the plain keep-back cut
	// (len(s)-13) is guaranteed to land 1 byte inside the final occurrence
	// rather than, by chance, on a boundary.
	reps := MaxSplitGuard/len(secretVal) + 100
	s := strings.Repeat(secretVal, reps)

	out, cut := v.FlushPoint(s)
	if cut == 0 {
		t.Fatal("FlushPoint made no progress above MaxSplitGuard: the buffer would grow without bound")
	}
	if out != Redacted {
		t.Fatalf("escape hatch must redact the flushed prefix as one opaque marker, got %q", out)
	}
	tail := s[cut:]
	if tail != secretVal {
		t.Fatalf("retained tail = %q, want the complete secret %q (cut must land at an occurrence boundary, not mid-occurrence)", tail, secretVal)
	}
	// What actually happens next -- a forced flush completing the line, or
	// Close at EOF -- forwards the tail through Redact exactly like this.
	if redacted := v.Redact(tail); redacted != Redacted {
		t.Fatalf("Redact(retained tail) = %q, want the tail fully hidden (it must be a complete occurrence)", redacted)
	}
}

// TestValuesFlushPointSingleHugeFormKeepsPlainCut pins the "snap > 0" guard
// the fix above needs: a second, ordinary tracked secret (never occurring in
// s) sets a real keep-back so cut lands strictly inside the huge form's one
// lone occurrence (which starts at 0, crossing cut, so the escape hatch's
// snap loop runs) -- but that occurrence is the only span, and it starts at
// 0 itself, so there is no smaller occurrence for the snap loop to advance
// to. The guard (snap > 0) must then leave cut exactly as computed, matching
// the pre-rd2, accepted behaviour for a form this large (see FlushPoint's
// doc, "Forms longer than MaxSplitGuard are left out of the keep-back"),
// rather than, say, zeroing it and stalling forward progress.
func TestValuesFlushPointSingleHugeFormKeepsPlainCut(t *testing.T) {
	t.Parallel()
	var v Values
	huge := strings.Repeat("Z", MaxSplitGuard+4096) // longer than MaxSplitGuard: excluded from the keep-back length
	v.Add([]byte(huge))
	other := "unrelated-tracked-secret-xyz" // 28 bytes, never occurs in s; only sets the keep-back
	v.Add([]byte(other))
	s := huge // one lone occurrence filling all of s, starting at 0

	out, cut := v.FlushPoint(s)
	if cut <= 0 {
		t.Fatal("FlushPoint made no progress on a single huge occurrence: the buffer would grow without bound")
	}
	if out != Redacted {
		t.Fatalf("escape hatch must redact the flushed prefix as one opaque marker, got %q", out)
	}
	// other's keep-back (len(other)-1) applies, and nothing shorter than the
	// huge occurrence itself starts inside it, so the snap loop finds
	// nothing to advance to and cut stays exactly at the plain keep-back.
	want := len(s) - (len(other) - 1)
	if cut != want {
		t.Fatalf("cut = %d, want %d (no smaller occurrence to snap to; keep the plain keep-back cut)", cut, want)
	}
}

// TestValuesFlushPointSingleModerateOccurrenceForcesStallNotLeak is a
// permanent regression test for the second gap the rd2 review found in an
// earlier version of this fix: its "snap > 0" guard fell back to the plain,
// potentially mid-occurrence keep-back cut whenever no smaller occurrence
// existed to snap to -- correct only for a form EXCLUDED from protection
// (longer than MaxSplitGuard, see
// TestValuesFlushPointSingleHugeFormKeepsPlainCut). A single, ordinary
// PROTECTED form (well under MaxSplitGuard) occurring exactly once, at
// offset 0, with nothing smaller to snap to, hits the very same
// "nothing to snap to" case -- but here the fallback must NOT be the plain
// cut: that cut lands deep inside the one and only occurrence, and once
// forwarded there is nothing left anywhere to complete the match against,
// so the retained fragment would leak raw forever. FlushPoint must instead
// make no progress at all on this call.
func TestValuesFlushPointSingleModerateOccurrenceForcesStallNotLeak(t *testing.T) {
	t.Parallel()
	var v Values
	moderate := strings.Repeat("m", 40000) // well under MaxSplitGuard (65536): a protected form
	v.Add([]byte(moderate))
	s := moderate + strings.Repeat("z", 30000) // > MaxSplitGuard total; one lone occurrence at offset 0

	out, cut := v.FlushPoint(s)
	if cut != 0 || out != "" {
		t.Fatalf("FlushPoint = (%q, %d), want (\"\", 0): the lone occurrence has nothing smaller to snap to, so any cut here would retain a raw fragment of it forever", out, cut)
	}
}

// TestProtectedCrossingRejectsUnsafeNearestSpanStart is a permanent
// regression test for the exact bug the rd2 review found in an earlier
// version of this fix's snap logic: snapping to "the nearest span's own
// start at or before cut" is not enough when a DIFFERENT, still-open span
// also crosses that same point. Two forms whose occurrences are [0,10) and
// [3,8) (a 10-byte form nesting a 5-byte one, e.g. "ABCDEFGHIJ" and its own
// substring "DEFGH") merge into one run; at cut=6, the buggy logic snapped
// to 3 (the second span's start) even though the first span's occurrence,
// [0,10), still crosses 3 just as much as it crosses 6 -- retaining bytes
// [8,10) of the first occurrence raw at the front of the tail once the
// (also fully redacted-looking) second span's text is stripped out ahead of
// it. protectedCrossing must instead report that no point below 10 is safe.
func TestProtectedCrossingRejectsUnsafeNearestSpanStart(t *testing.T) {
	t.Parallel()
	spans := [][2]int{{0, 10}, {3, 8}}
	if snap, crossed := protectedCrossing(spans, 6); snap != 0 || !crossed {
		t.Fatalf("protectedCrossing(spans, 6) = (%d, %v), want (0, true): no safe point below 10 exists (the [0,10) span still crosses it)", snap, crossed)
	}
	if snap, crossed := protectedCrossing(spans, 9); snap != 0 || !crossed {
		t.Fatalf("protectedCrossing(spans, 9) = (%d, %v), want (0, true): the [0,10) span still crosses 9", snap, crossed)
	}
	if snap, crossed := protectedCrossing(spans, 10); snap != 10 || crossed {
		t.Fatalf("protectedCrossing(spans, 10) = (%d, %v), want (10, false): both occurrences are fully cleared at 10", snap, crossed)
	}
}

// TestProtectedCrossingFindsInteriorTouchBoundary complements the above: two
// forms whose occurrences merely TOUCH rather than overlap ([0,3) and
// [3,6), e.g. two distinct three-byte forms placed back to back) do have a
// genuine safe interior boundary — at their shared touch point, 3, and
// again at the run's own end, 6 — which protectedCrossing must find (the
// largest one at or below cut), not just the trivial 0.
func TestProtectedCrossingFindsInteriorTouchBoundary(t *testing.T) {
	t.Parallel()
	spans := [][2]int{{0, 3}, {3, 6}}
	if snap, crossed := protectedCrossing(spans, 6); snap != 6 || crossed {
		t.Fatalf("protectedCrossing(spans, 6) = (%d, %v), want (6, false)", snap, crossed)
	}
	// The second span [3,6) itself still crosses 5 (crossed = true), but the
	// touch point at 3 is a genuine safe boundary below it, and FlushPoint's
	// caller checks snap > 0 before crossed, so this is still real progress.
	if snap, crossed := protectedCrossing(spans, 5); snap != 3 || !crossed {
		t.Fatalf("protectedCrossing(spans, 5) = (%d, %v), want (3, true)", snap, crossed)
	}
}

// TestProtectedCrossingFindsFarEdgeOfGap is a permanent regression test for
// a bug the rd2 review's second round found: an earlier version of
// protectedCrossing recorded only frontier (a safe gap's NEAR edge) as the
// snap candidate, instead of the largest point in that gap at or below cut
// (its FAR edge — the next span's own start, or cut itself once nothing
// further constrains it). That under-reported the truly safe cut: for
// spans [0,5) and [20,25) with cut=15, nothing crosses 15 at all (the gap
// between the two spans covers it entirely), so 15 itself is safe, but the
// buggy version returned only 5. This cannot leak (a smaller-than-optimal
// safe point is still safe), but it can make a large, entirely ordinary
// multi-line secret whose own body lines are separately tracked as short
// protected forms scattered through it (see strongLines) advance only a
// handful of bytes per FlushPoint call despite most of the gap between
// them being genuinely open — compounding the quadratic rescan cost
// protectedCrossing's doc describes for a real, non-adversarial input.
func TestProtectedCrossingFindsFarEdgeOfGap(t *testing.T) {
	t.Parallel()
	spans := [][2]int{{0, 5}, {20, 25}}
	if snap, crossed := protectedCrossing(spans, 15); snap != 15 || crossed {
		t.Fatalf("protectedCrossing(spans, 15) = (%d, %v), want (15, false): the gap between the two spans clears 15 entirely", snap, crossed)
	}
	if snap, crossed := protectedCrossing(spans, 30); snap != 30 || crossed {
		t.Fatalf("protectedCrossing(spans, 30) = (%d, %v), want (30, false): nothing crosses 30 either", snap, crossed)
	}
	// A cut genuinely inside the second span must still be rejected.
	if snap, crossed := protectedCrossing(spans, 22); snap != 20 || !crossed {
		t.Fatalf("protectedCrossing(spans, 22) = (%d, %v), want (20, true)", snap, crossed)
	}
}

func TestValuesRedactLongestFirst(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("inner-secret"))
	v.Add([]byte("outer-inner-secret-tail"))
	got := v.Redact("a outer-inner-secret-tail b inner-secret c")
	if want := "a " + Redacted + " b " + Redacted + " c"; got != want {
		t.Fatalf("Redact = %q, want %q", got, want)
	}
	if strings.Contains(got, "tail") || strings.Contains(got, "outer") {
		t.Fatalf("a longer secret was left half visible: %q", got)
	}
}

func TestValuesRedactJSONEscaped(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte(fakeTokenValue))
	line, err := json.Marshal(map[string]string{"k": "x" + fakeTokenValue})
	if err != nil {
		t.Fatal(err)
	}
	got := v.Redact(string(line))
	if v.Contains([]byte(got)) || !strings.Contains(got, Redacted) {
		t.Fatalf("Redact left the JSON-escaped value: %s", got)
	}
}

func TestValuesEmptyAndReset(t *testing.T) {
	t.Parallel()
	var v Values
	if !v.Empty() || v.Contains([]byte("anything")) {
		t.Fatal("zero Values must be empty")
	}
	v.Add(nil)
	v.Add([]byte(" \n"))
	if v.Contains([]byte("x")) {
		t.Fatal("whitespace-only value must only match itself")
	}
	v.Add([]byte(fakeTokenValue))
	v.Reset()
	if !v.Empty() || v.Contains([]byte(fakeTokenValue)) {
		t.Fatal("Reset must forget every value")
	}
}

// TestValuesConcurrentUse runs Add, Contains and Redact from several
// goroutines; -race proves the locking.
func TestValuesConcurrentUse(t *testing.T) {
	t.Parallel()
	var v Values
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value := strings.Repeat("s", 5+i)
			v.Add([]byte(value))
			_ = v.Contains([]byte("x" + value))
			_ = v.Redact("y" + value)
		}()
	}
	wg.Wait()
	if !v.Contains([]byte(strings.Repeat("s", 12))) {
		t.Fatal("a concurrently added value is missing")
	}
}

// BenchmarkValuesFlushPointDenseChain measures FlushPoint's cost on the
// realistic shape task le2's annotation measured: a credentials file's
// "====...=" divider line (here added directly as the tracked secret,
// standing in for the redact-only form strongLines would derive from a real
// multi-line secret) against an unterminated "="-only progress-bar line,
// which never gives the escape hatch a gap to snap to. Run at a few sizes
// (go test -bench BenchmarkValuesFlushPointDenseChain -benchtime <n>x) to see
// the shape of the cost: pre-flushStallCap (task le2) this grew roughly with
// the square of b.N's byte count once past MaxSplitGuard, matching the
// measured 128 KiB/512 KiB/2 MiB numbers in the task's annotation; with the
// cap, cost per byte stays bounded because the escape hatch never scans past
// flushStallCap before forcing a fresh start.
func BenchmarkValuesFlushPointDenseChain(b *testing.B) {
	for _, size := range []int{MaxSplitGuard / 2, 2 * MaxSplitGuard, 8 * MaxSplitGuard} {
		b.Run(fmt.Sprintf("%dKiB", size/1024), func(b *testing.B) {
			var v Values
			v.Add([]byte(strings.Repeat("=", 40)))
			s := strings.Repeat("=", size)
			b.ResetTimer()
			for range b.N {
				v.FlushPoint(s)
			}
		})
	}
}

// simulateRelay reproduces internal/logger.RedactingWriter's own
// Write/forwardSafePrefix/Close loop directly against v, so a test can
// measure the real caller contract rather than calling FlushPoint in
// isolation: secret must not import internal/logger (see that package's own
// doc comment on the import cycle this would close through
// internal/safepath and internal/testutil), so the loop is reimplemented
// here rather than imported. data is fed in chunk-sized writes, exactly like
// a relayed child's output arriving through io.Copy's 32 KiB default
// buffer; each completed line is forwarded through Redact, and an overlong
// unterminated remainder is forced through FlushPoint exactly as
// forwardSafePrefix does (out written as FlushPoint returned it, never
// re-derived — see forwardSafePrefix's own doc for why); whatever remains
// pending once data runs out is forwarded through Redact, mirroring Close.
// It returns everything forwarded, concatenated, and the largest pending
// buffer ever held at once (invariant B's own measurement).
func simulateRelay(v *Values, data []byte, chunk int) (forwarded string, maxPending int) {
	var out strings.Builder
	var pending []byte
	for start := 0; start < len(data); start += chunk {
		end := min(start+chunk, len(data))
		pending = append(pending, data[start:end]...)
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				break
			}
			out.WriteString(v.Redact(string(pending[:i+1])))
			pending = pending[i+1:]
		}
		if len(pending) > v.MaxPending() {
			redOut, consumed := v.FlushPoint(string(pending))
			consumed = min(max(consumed, 0), len(pending))
			if consumed > 0 {
				out.WriteString(redOut)
				pending = append([]byte(nil), pending[consumed:]...)
			}
		}
		maxPending = max(maxPending, len(pending))
	}
	if len(pending) > 0 {
		out.WriteString(v.Redact(string(pending)))
	}
	return out.String(), maxPending
}

// TestValuesFlushPointHistoricalShapesBoundedAndLeakFree is a permanent,
// consolidated regression test for every shape FlushPoint's escape hatch has
// broken on across its rounds of fixes so far (mb2, 3d2, rd2, le2, 1g2,
// sg2, tg2). It drives each shape through simulateRelay (the same
// Write/forwardSafePrefix/Close loop a real relay uses) at four sizes
// spanning well below and well past flushStallCap, and checks BOTH
// invariants this one function must hold AT ONCE, in one place, per task
// 1g2's explicit request: every round so far fixed one of these two while
// breaking the other, and no single test in this suite asserted both
// together across every historical shape before this one.
//   - Invariant (A): no raw fragment of any tracked secret ever reaches the
//     forwarded output. Checked per shape by that shape's own leak probe
//     (a substring that can only appear if a real occurrence's bytes were
//     forwarded without being replaced by Redacted).
//   - Invariant (B): the retained pending buffer never exceeds a generous
//     but still-bounded ceiling (flushStallCap, plus the shape's own
//     longest protected form, plus one chunk of in-flight slack), and the
//     relay's cost grows roughly linearly with size
//     (checkHistoricalShapeLinearCost). When the input grows 8x, from
//     flushStallCap to 2 MiB, cost linear in size (the actual target of
//     every fix in this file) grows about 8x, while a quadratic blow-up
//     (mb2's and le2's own regressions, both confirmed to take seconds per
//     MiB once unbounded) grows it about 50x. The cost is linear here only
//     because the chunk size and the shapes' forms are fixed: FlushPoint's
//     "Cost" paragraph has how smaller writes raise the per-byte factor.
//
// Invariant (B)'s cost check used to be an absolute wall-clock deadline per
// size (5s plus 3s per MiB). That failed under -race whenever other suites
// loaded the machine, even on unchanged code (task 2h2), so it is now a
// same-process cost ratio measured in thread CPU time (see
// checkHistoricalShapeLinearCost).
func TestValuesFlushPointHistoricalShapesBoundedAndLeakFree(t *testing.T) {
	t.Parallel()

	shapes := []historicalShape{
		{
			// mb2 / le2: a credentials file's divider line against an
			// unterminated "="-only progress bar. The tracked form is
			// uniform (every byte the same), so occurrences overlap at
			// literally every offset -- the densest possible
			// self-overlapping chain, which never gives the escape
			// hatch a gap to snap to anywhere in the filler.
			name: "divider-vs-progress-bar",
			build: func(total int) (*Values, []byte, []byte) {
				v := &Values{}
				v.Add([]byte(strings.Repeat("=", 40)))
				return v, []byte(strings.Repeat("=", total)), nil
			},
			leaked: func(forwarded string) bool {
				return strings.Contains(strings.ReplaceAll(forwarded, Redacted, ""), strings.Repeat("=", 40))
			},
		},
		{
			// 3d2 / 1g2: a shorter, periodic secret that is also the
			// exact prefix of a longer one -- the shape that leaked in
			// two different ways (3d2's run[1] cut, then 1g2's
			// cut=len(s)) across two different fixes to this same
			// escape hatch.
			name: "periodic-prefix-of-longer",
			build: func(total int) (*Values, []byte, []byte) {
				v := &Values{}
				shorter := "x1x1x1x1x1"
				tail := strings.Repeat("Q", 30)
				v.Add([]byte(shorter))
				v.Add([]byte(shorter + tail))
				// filler must stay an even number of bytes: the "x1"
				// pattern only reads as complete shorter/longer
				// occurrences at even offsets, so an odd filler length
				// shifts tail out of phase and the longer form never
				// actually occurs (sizes here are all even already,
				// and len(tail) is even, so this holds for every size).
				filler := total - len(tail)
				return v, []byte(strings.Repeat("x1", filler/2+1)[:filler]), []byte(tail)
			},
			leaked: func(forwarded string) bool {
				return strings.Contains(forwarded, strings.Repeat("Q", 10))
			},
		},
		{
			// rd2: a single strong, non-periodic secret repeated
			// back-to-back, forming one merged run purely from
			// touching (not overlapping) occurrences.
			name: "repeated-single-secret",
			build: func(total int) (*Values, []byte, []byte) {
				v := &Values{}
				secretVal := "db-password-42"
				v.Add([]byte(secretVal))
				reps := total / len(secretVal)
				return v, []byte(strings.Repeat(secretVal, reps)), nil
			},
			leaked: func(forwarded string) bool {
				return strings.Contains(strings.ReplaceAll(forwarded, Redacted, ""), "password")
			},
		},
		{
			// 3d2 / 1g2 verbatim: a short prefix token nested inside a
			// longer credential that starts with it -- named in task
			// 1g2's own annotation as reproduced at the 3d2 round and
			// again here (the "AAAA"/"AAAAdb-password-42" shape). This
			// is the shape task 1g2's own round-2 review found: the
			// filler-only buffer ends in a way that makes a COMPLETE
			// occurrence of the longer, 18-byte form land exactly where
			// the plain keep-back cut would fall once tail is appended.
			name: "prefix-token-nested-in-credential",
			build: func(total int) (*Values, []byte, []byte) {
				v := &Values{}
				v.Add([]byte("AAAA"))
				v.Add([]byte("AAAAdb-password-42"))
				tail := "db-password-42"
				filler := total - len(tail)
				return v, []byte(strings.Repeat("A", filler)), []byte(tail)
			},
			leaked: func(forwarded string) bool {
				return strings.Contains(forwarded, "db-password-42")
			},
		},
		{
			// A four-level nested chain (n1 inside n2 inside n3 inside
			// n4, each tracked separately) appended after a dense
			// filler of n1's own periodic pattern -- stresses
			// protectedCrossing across several simultaneously-open
			// protected spans of different lengths at once, not just
			// two.
			name: "four-level-nested-chain",
			build: func(total int) (*Values, []byte, []byte) {
				v := &Values{}
				n1 := "N1N1"
				n2 := n1 + "N2N2"
				n3 := n2 + "N3N3"
				n4 := n3 + "N4N4"
				v.Add([]byte(n1))
				v.Add([]byte(n2))
				v.Add([]byte(n3))
				v.Add([]byte(n4))
				tail := "N2N2N3N3N4N4"
				filler := total - len(tail)
				return v, []byte(strings.Repeat("N1", filler/2+1)[:filler]), []byte(tail)
			},
			leaked: func(forwarded string) bool {
				stripped := strings.ReplaceAll(forwarded, Redacted, "")
				return strings.Contains(stripped, "N2N2") || strings.Contains(stripped, "N3N3") || strings.Contains(stripped, "N4N4")
			},
		},
		{
			// task 1g2 round 3 (found by independent review during this
			// task's own self-review, before round 2's fix ever
			// shipped): a periodic filler driver p, the registry's
			// longest tracked form l ending mid-buffer, and a THIRD,
			// shorter, UNRELATED form m whose occurrence overlaps l's
			// own tail and extends past l's end. Resolving past only
			// occurrences of length == longest (an earlier version of
			// the round-2 fix) stops at l's end and leaves m split;
			// resolving past any occurrence that is not itself a
			// prefix of something longer -- the actual fix -- resolves
			// past both in one pass, since m is not a prefix of l or p
			// either.
			name: "third-form-overlaps-maximal-occurrence",
			build: func(total int) (*Values, []byte, []byte) {
				v := &Values{}
				p := "ABAB"
				l := "ABAB" + strings.Repeat("Q", 16) // 20 bytes: longest
				m := "QQQQQ" + "42Pas"                // 10 bytes: overlaps l's tail
				v.Add([]byte(p))
				v.Add([]byte(l))
				v.Add([]byte(m))
				tail := strings.Repeat("Q", 16) + "42Pas" // l's non-filler suffix + m's own trailing bytes
				filler := total - len(tail)
				return v, []byte(strings.Repeat("AB", filler/2+1)[:filler]), []byte(tail)
			},
			leaked: func(forwarded string) bool {
				return strings.Contains(forwarded, "42Pas")
			},
		},
		{
			// task 1g2 round 4: p ("AAAA") is both the filler driver AND
			// q's own prefix, forcing crossed==true exactly like round
			// 2's shape. q ("AAAA"+"Y"*16) is the registry's own longest
			// tracked form, so a span-level "is q's full text a prefix
			// of something longer" check (round 3's fix) judges it
			// trivially safe to resolve past in full. But n
			// ("Y"*8+"ZZZZ") is a separate, shorter tracked form whose
			// forming prefix hides inside q's own last 8 bytes -- a
			// span-level check never considers a form starting partway
			// through another span, only found by checking every
			// trailing-byte-count of s directly against every tracked
			// form's own prefix (longestKeepBackForForcedFlush).
			name: "fourth-form-forming-prefix-mid-span",
			build: func(total int) (*Values, []byte, []byte) {
				v := &Values{}
				p := "AAAA"
				q := "AAAA" + strings.Repeat("Y", 16) // 20 bytes: registry's own longest
				n := strings.Repeat("Y", 8) + "ZZZZ"  // 12 bytes: hides inside q's own last 8 bytes
				v.Add([]byte(p))
				v.Add([]byte(q))
				v.Add([]byte(n))
				tail := strings.Repeat("Y", 16) + "ZZZZ" // q's non-filler suffix + n's own completing bytes
				filler := total - len(tail)
				return v, []byte(strings.Repeat("A", filler)), []byte(tail)
			},
			leaked: func(forwarded string) bool {
				return strings.Contains(strings.ReplaceAll(forwarded, Redacted, ""), "ZZZZ")
			},
		},
		{
			// task tg2: the driver "RMNOP"+"RMNOP" plus two nested forms,
			// "MNOP" and "PRMN", whose occurrences tile the driver's unit
			// between them, so no gap-free cut exists anywhere. h is a
			// proper-prefix form matching the stream's last 12 bytes
			// whenever a buffer ends at a unit boundary, retreating the
			// forced cut by 12 bytes into the tiling. Before tg2, the
			// relay forwarded a split "MNOP"'s raw "O" (and a split
			// driver's "R"). The stream is whole units, so every byte lies
			// inside a driver occurrence: any raw letter is a leak.
			name: "nested-forms-tile-driver-unit",
			build: func(total int) (*Values, []byte, []byte) {
				v := &Values{}
				unit := "RMNOP"
				v.Add([]byte(unit + unit))
				v.Add([]byte("MNOP"))
				v.Add([]byte("PRMN"))
				v.Add([]byte("OPRMNOPRMNOP" + "X"))
				return v, []byte(strings.Repeat(unit, total/len(unit))), nil
			},
			leaked: func(forwarded string) bool {
				return strings.ContainsAny(strings.ReplaceAll(forwarded, Redacted, ""), "RMNOP")
			},
		},
	}

	sizes := []int{80 << 10, 256 << 10, 512 << 10, 2 << 20} // below flushStallCap, at it, and twice further past it
	largest := sizes[len(sizes)-1]

	for _, sh := range shapes {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s/%dKiB", sh.name, size>>10), func(t *testing.T) {
				checkHistoricalShapeSplitPath(t, sh, size)
				checkHistoricalShapeDirectPath(t, sh, size)
				relayCost := checkHistoricalShapeRelayPath(t, sh, size)
				// The cost ratio is taken once per shape, at the largest
				// size, against a fresh run at 1/linearScaleFactor of it
				// (see linearScaleFactor for why that baseline sits at
				// flushStallCap).
				if size == largest {
					checkHistoricalShapeLinearCost(t, sh, size, relayCost)
				}
			})
		}
	}
}

// historicalShapeChunk is io.Copy's default buffer size, matching a real
// relay's chunking; TestValuesFlushPointHistoricalShapesBoundedAndLeakFree's
// relay runs feed their data in writes of this size.
const historicalShapeChunk = 32 << 10

// historicalShape is one shape FlushPoint's escape hatch has broken on, as
// driven by TestValuesFlushPointHistoricalShapesBoundedAndLeakFree.
type historicalShape struct {
	name string
	// build returns a fresh Values with this shape's forms tracked, and
	// total bytes of data split into filler (the bulk, always safe on its
	// own) and tail (the trailing bytes that only resolve into a
	// leak-relevant secret once combined with filler's own trailing
	// bytes). tail is empty for a shape with no distinct nested/trailing
	// secret of its own.
	build func(total int) (v *Values, filler, tail []byte)
	// leaked reports whether forwarded holds a raw fragment that could
	// only appear via a leak of this shape's secret material.
	leaked func(forwarded string) bool
}

// checkHistoricalShapeSplitPath is invariant (A)'s cross-call check (task
// 1g2 round 1's shape): it calls FlushPoint on the FILLER ALONE first -- the
// tail has not arrived yet, exactly like a relay whose child hasn't written
// the rest of the line -- forcing the cap or the dense chain to resolve
// without the tail in view. Only afterward is the retained remainder
// combined with tail and redacted, mimicking the tail arriving on a later
// Write. This is the shape every dedicated 3d2/rd2/1g2-round-1 regression
// test uses, and is essential: with the tail already baked into one buffer
// (the direct and relay checks), a capped flush can swallow filler and tail
// together in one opaque marker with nothing left pending to leak later,
// which does not exercise this failure mode at all.
func checkHistoricalShapeSplitPath(t *testing.T, sh historicalShape, size int) {
	t.Helper()
	v, filler, tail := sh.build(size)
	out, consumed := v.FlushPoint(string(filler))
	var forwarded strings.Builder
	remainder := filler
	if consumed > 0 {
		forwarded.WriteString(out)
		remainder = filler[consumed:]
	}
	remainder = append(append([]byte(nil), remainder...), tail...)
	forwarded.WriteString(v.Redact(string(remainder)))
	if sh.leaked(forwarded.String()) {
		t.Fatalf("invariant (A) violated on the split (filler-then-tail) path: a raw secret fragment leaked into forwarded output (%d of %d bytes)", forwarded.Len(), size)
	}
}

// checkHistoricalShapeDirectPath is invariant (A)'s within-call check (task
// 1g2 round 2's shape): filler and tail are already combined into ONE buffer
// before the very first FlushPoint call, so a complete occurrence ending
// exactly where the naive keep-back cut would fall is visible from the
// start -- the shape every dedicated round-2 repro (see
// extendPastCompleteLongestOccurrences) uses.
func checkHistoricalShapeDirectPath(t *testing.T, sh historicalShape, size int) {
	t.Helper()
	v, filler, tail := sh.build(size)
	data := append(append([]byte(nil), filler...), tail...)
	out, consumed := v.FlushPoint(string(data))
	forwarded := out
	if consumed > 0 && consumed < len(data) {
		forwarded += v.Redact(string(data[consumed:]))
	} else if consumed <= 0 {
		forwarded = v.Redact(string(data))
	}
	if sh.leaked(forwarded) {
		t.Fatalf("invariant (A) violated on the direct single-call path: a raw secret fragment leaked into forwarded output (%d of %d bytes)", len(forwarded), size)
	}
}

// checkHistoricalShapeRelayPath runs the realistic end-to-end path, chunked
// exactly like a real relay (historicalShapeChunk), and checks invariant (A)
// on its output plus invariant (B)'s pending-buffer bound. It returns the
// run's cost (measureCost) for checkHistoricalShapeLinearCost. The bound is
// reported with Errorf, not Fatalf, so a regression that breaks both halves
// of invariant (B) at once (an unbounded stall grows pending AND makes the
// cost quadratic) is reported by both checks rather than hiding the cost
// check behind the bound.
func checkHistoricalShapeRelayPath(t *testing.T, sh historicalShape, size int) time.Duration {
	t.Helper()
	v, filler, tail := sh.build(size)
	data := append(append([]byte(nil), filler...), tail...)
	var forwarded string
	var maxPending int
	cost := measureCost(t, func() {
		forwarded, maxPending = simulateRelay(v, data, historicalShapeChunk)
	})

	if sh.leaked(forwarded) {
		t.Fatalf("invariant (A) violated: a raw secret fragment leaked into forwarded output (%d of %d bytes)", len(forwarded), size)
	}

	longest := 0
	for _, e := range v.snapshot() {
		if e.contained && len(e.form) <= MaxSplitGuard {
			longest = max(longest, len(e.form))
		}
	}
	bound := flushStallCap + longest + historicalShapeChunk
	if maxPending > bound {
		t.Errorf("invariant (B) violated: pending reached %d bytes, want <= %d (flushStallCap=%d + longest=%d + chunk=%d)", maxPending, bound, flushStallCap, longest, historicalShapeChunk)
	}
	return cost
}

// Invariant (B)'s cost-scaling limits (checkHistoricalShapeLinearCost).
// The baseline run is 1/linearScaleFactor of the largest size, 256 KiB =
// flushStallCap: it never passes the cap, so its cost is one stall cycle
// (pending growing to the cap) that every correct or broken escape hatch
// pays alike, while the largest run is about linearScaleFactor such cycles
// when the cap bounds pending. Linear cost therefore scales by about
// linearScaleFactor (measured 7.3x-10x across the shapes under -race), and
// a quadratic regression by far more, since only the larger run exposes it
// (an unbounded-stall mutation of escapeHatchFlush, le2's regression,
// measured 48x-63x). A baseline past the cap would itself be inflated by
// the regression and shrink that gap: at a 4x step (512 KiB vs 2 MiB) the
// same mutation measured as little as 7x against 4x for linear cost.
// maxLinearCostRatio sits between the two measured ranges with a margin of
// about 2x to each, leaving room for noise either way.
// linearCostSlack is an absolute allowance on top: for the shapes whose
// whole run costs only tens of milliseconds (repeated-single-secret),
// scheduler and GC noise is a large fraction of the figure, while a
// quadratic run of any shape here costs seconds and dwarfs it.
const (
	linearScaleFactor  = 8
	maxLinearCostRatio = 20
	linearCostSlack    = 150 * time.Millisecond
	linearCostAttempts = 3
)

// historicalShapeRelayCost measures one fresh relay run of shape sh at size.
func historicalShapeRelayCost(t *testing.T, sh historicalShape, size int) time.Duration {
	t.Helper()
	v, filler, tail := sh.build(size)
	data := append(append([]byte(nil), filler...), tail...)
	return measureCost(t, func() {
		simulateRelay(v, data, historicalShapeChunk)
	})
}

// checkHistoricalShapeLinearCost is invariant (B)'s cost check. It compares
// relayCost, the relay run at size, against a fresh relay run of the same
// shape at size/linearScaleFactor, and fails if the larger run costs more
// than maxLinearCostRatio times the smaller one (plus linearCostSlack):
// roughly linear cost passes, a quadratic blow-up fails.
//
// It asserts a ratio of two measurements taken in the same process moments
// apart, not an absolute deadline, and measures thread CPU time
// (measureCost), not wall-clock time: the absolute wall-clock deadlines it
// replaced failed under -race whenever other suites loaded the machine,
// even on unchanged code (task 2h2). CPU time the scheduler gives to other
// processes or goroutines is not counted, and a slowdown that affects both
// runs alike (the race detector, a busy sibling hyperthread, a slower
// clock) cancels out of the ratio.
func checkHistoricalShapeLinearCost(t *testing.T, sh historicalShape, size int, relayCost time.Duration) {
	t.Helper()
	baseSize := size / linearScaleFactor
	baseCost := historicalShapeRelayCost(t, sh, baseSize)
	for range linearCostAttempts - 1 {
		baseCost = min(baseCost, historicalShapeRelayCost(t, sh, baseSize))
	}
	limit := maxLinearCostRatio*baseCost + linearCostSlack
	// A VM whose host steals time mid-run can inflate one measurement
	// several-fold (seen on the OpenBSD CI VM). Re-measure the large run
	// before failing and keep the cheapest: noise only ever adds cost,
	// while a quadratic blow-up exceeds the limit on every attempt.
	for i := 1; i < linearCostAttempts && relayCost > limit; i++ {
		relayCost = min(relayCost, historicalShapeRelayCost(t, sh, size))
	}
	t.Logf("relay cost: %s for %d bytes vs %s for %d bytes (ratio %.2f, limit %s)", relayCost, size, baseCost, baseSize, float64(relayCost)/float64(max(baseCost, 1)), limit)
	if relayCost > limit {
		t.Errorf("invariant (B) violated: relay cost %s for %d bytes exceeds %dx the %s for %d bytes plus %s slack (limit %s): cost grows faster than linearly (quadratic?)", relayCost, size, maxLinearCostRatio, baseCost, baseSize, linearCostSlack, limit)
	}
}

// TestValuesFlushPointDividerTailAlignmentSweep is a permanent regression
// test for the exact breadth the task 1g2 annotation's own probe reported:
// a "="*40 divider registered alongside a longer form ending in
// "TAILSECRET99" leaked at 24 of 40 tested filler alignments against the
// then-shipped code (0 of 40 at the pre-le2 parent). It sweeps filler
// length across many alignments (more than the original 40) at a size past
// flushStallCap, and asserts zero leaks at every one -- not just the one
// alignment the other shapes above happen to hit.
func TestValuesFlushPointDividerTailAlignmentSweep(t *testing.T) {
	t.Parallel()
	const divider = "===================================" + "=====" // 40 bytes, self-overlapping
	const tail = "TAILSECRET99"
	if len(divider) != 40 {
		t.Fatalf("test setup: len(divider) = %d, want 40", len(divider))
	}
	longer := divider + tail // 52 bytes

	leakCount := 0
	for alignment := range 64 {
		v := &Values{}
		v.Add([]byte(divider))
		v.Add([]byte(longer))

		base := flushStallCap + 4096 // comfortably past the cap
		filler := strings.Repeat("=", base+alignment)

		// Split path: filler alone first (tail not yet arrived), then
		// combine whatever's retained with the tail -- the shape that
		// actually exercises the cap-forced flush before the longer
		// occurrence is even visible.
		out, consumed := v.FlushPoint(filler)
		var forwarded strings.Builder
		var remainder string
		if consumed > 0 {
			forwarded.WriteString(out)
			remainder = filler[consumed:]
		} else {
			remainder = filler
		}
		forwarded.WriteString(v.Redact(remainder + tail))

		if strings.Contains(forwarded.String(), tail) {
			leakCount++
			t.Errorf("alignment %d: raw tail leaked: ...%q", alignment, forwarded.String()[max(0, forwarded.Len()-40):])
		}
	}
	if leakCount > 0 {
		t.Fatalf("%d of 64 alignments leaked (want 0; task 1g2's annotation reported 24 of 40 leaking pre-fix)", leakCount)
	}
}

// TestValuesFlushPointThirdFormOverlapsMaximalOccurrence is a permanent
// regression test for task 1g2 round 3: an independent review, performed as
// part of this task's own mandated self-review BEFORE round 2's fix ever
// shipped, found that extending cut past only occurrences of the registry's
// single longest tracked length is not enough. With three (or more) tracked
// forms, resolving past one maximal-length occurrence can land the
// resulting cut inside a SEPARATE, shorter, unrelated occurrence that
// independently overlaps and extends past the maximal one's own end,
// leaving THAT occurrence split instead -- the same class of leak as round
// 2's, just one level removed. Shapes:
//   - p ("ABAB", 4 bytes): a periodic filler driver, matching at every even
//     offset in a repeated "AB" stream, so it forms one dense,
//     self-overlapping, gapless chain from offset 0 -- the same mechanism
//     every crossed/capped shape in this file relies on to reach the escape
//     hatch at all.
//   - l ("ABAB"+"Q"*16, 20 bytes): the registry's longest tracked form,
//     landing so its naive keep-back cut falls inside it (exactly like
//     round 2's "AAAAdb-password-42").
//   - m ("QQQQQ"+"42Pas", 10 bytes): a shorter, INDEPENDENT tracked form
//     that is NOT a prefix of l or p, whose occurrence starts inside l's
//     own tail and extends 5 bytes past l's end.
//
// A version of the fix that only extends past occurrences of length ==
// longest resolves past l (landing cut at l's own end) but then leaves m
// split there, stranding m's trailing "42Pas" -- unrecognisable on its own
// -- in pending forever. The fix that extends past any occurrence NOT a
// prefix of something longer (regardless of its length) resolves past both
// l and m in the same single pass, since m is not a prefix of any longer
// tracked form either.
func TestValuesFlushPointThirdFormOverlapsMaximalOccurrence(t *testing.T) {
	t.Parallel()
	v := &Values{}
	p := "ABAB"
	l := "ABAB" + strings.Repeat("Q", 16) // 20 bytes: longest
	m := "QQQQQ" + "42Pas"                // 10 bytes: overlaps l's tail, extends past it
	v.Add([]byte(p))
	v.Add([]byte(l))
	v.Add([]byte(m))

	fillerLen := flushStallCap
	filler := strings.Repeat("AB", fillerLen/2+1)[:fillerLen]
	s := filler + l + "42Pas" // l immediately followed by m's own trailing bytes
	if len(s) <= flushStallCap {
		t.Fatalf("test setup: len(s) = %d, want > flushStallCap (%d)", len(s), flushStallCap)
	}

	out, consumed := v.FlushPoint(s)
	if out != Redacted {
		t.Fatalf("FlushPoint = (%q, %d), want the single opaque marker %q as out", out, consumed, Redacted)
	}
	if consumed < len(s) {
		tail := s[consumed:]
		red := v.Redact(tail)
		if red == tail && tail != "" {
			t.Fatalf("retained tail %q forwarded completely unredacted: raw fragment of a tracked secret leaked (task 1g2 round 3)", tail)
		}
		if strings.Contains(red, "42Pas") {
			t.Fatalf("m's trailing bytes leaked raw: retained tail %q, Redact() = %q", tail, red)
		}
	}
}

// TestValuesFlushPointForthFormFormingPrefixMidSpan is a permanent
// regression test for task 1g2 round 4, found through this task's own
// hand-derivation immediately after round 3 (extendPastResolvableOccurrences)
// landed, before it ever shipped: round 3 judged a matched SPAN safe to
// extend past whenever that span's own FULL text was not a prefix of some
// other, longer tracked form -- but a DIFFERENT tracked form's forming
// prefix can start PARTWAY THROUGH that span, not at the span's own start,
// which a span-level check never examines at all.
//
// Shapes: p ("AAAA", 4 bytes) is both the periodic filler driver AND q's
// own prefix, so the filler's dense "AAAA" chain flows seamlessly into q
// with no natural gap, forcing crossed==true exactly like round 2's
// "AAAAdb-password-42" shape. q ("AAAA"+"Y"*16, 20 bytes) is the
// registry's own longest tracked form, so round 3's check judged it
// trivially safe to extend past (nothing is longer than it). But n
// ("Y"*8+"ZZZZ", 12 bytes) is a separate, shorter tracked form whose
// forming prefix ("Y"*8) is hiding inside q's own last 8 bytes -- n is
// longer than that specific 8-byte chunk, but shorter than q as a whole,
// so round 3's "is q's full text a prefix of something longer" check
// never considered n at all. Round 3's fix swept q's entire 20 bytes,
// including n's forming prefix, stranding it; the byte-suffix fix
// (longestKeepBackForForcedFlush) correctly retains exactly the last 8
// bytes ("YYYYYYYY"), independent of q's own span boundaries.
func TestValuesFlushPointForthFormFormingPrefixMidSpan(t *testing.T) {
	t.Parallel()
	v := &Values{}
	p := "AAAA"
	q := "AAAA" + strings.Repeat("Y", 16) // 20 bytes: registry's own longest
	n := strings.Repeat("Y", 8) + "ZZZZ"  // 12 bytes: matches q's own last 8 bytes as ITS prefix
	v.Add([]byte(p))
	v.Add([]byte(q))
	v.Add([]byte(n)) // n's own completion never arrives in this call

	filler := strings.Repeat("A", flushStallCap)
	s := filler + q // q ends exactly at len(s)
	if len(s) <= flushStallCap {
		t.Fatalf("test setup: len(s) = %d, want > flushStallCap (%d)", len(s), flushStallCap)
	}

	out, consumed := v.FlushPoint(s)
	if out != Redacted {
		t.Fatalf("FlushPoint = (%q, %d), want the single opaque marker %q as out", out, consumed, Redacted)
	}

	qStart := len(s) - len(q)
	nPrefixStart := qStart + 12 // q's offset 12: where the last 8 "Y"s (== n's forming prefix) begin
	if consumed > nPrefixStart {
		t.Fatalf("consumed=%d swept past n's forming-prefix start (%d): if n's remaining bytes ('ZZZZ') arrive on a later call, they leak raw with no 'YYYYYYYY' prefix left in pending to recognise them by (task 1g2 round 4)", consumed, nPrefixStart)
	}
	// Positive check: once n's completion DOES arrive (a later write), the
	// retained tail combined with it must be fully recognised and
	// redacted -- the retained tail alone is deliberately NOT expected to
	// redact to anything on its own here, since by construction it is
	// only n's still-incomplete forming prefix ("YYYYYYYY"), not a
	// complete occurrence of anything; asserting otherwise would be
	// testing the wrong thing.
	if consumed < len(s) {
		tail := s[consumed:]
		completed := tail + "ZZZZ" // n's own completing bytes, arriving on a later write
		red := v.Redact(completed)
		if strings.Contains(red, "ZZZZ") {
			t.Fatalf("n's completion did not get redacted once combined with the retained tail: retained=%q, Redact(retained+\"ZZZZ\")=%q", tail, red)
		}
		if !strings.Contains(red, Redacted) {
			t.Fatalf("completed secret was not redacted at all: %q", red)
		}
	}
}

// TestValuesFlushPointDriverExemptionResolvesEmbeddedFormRegression is a
// permanent regression test for task sg2 (task 1g2 round 5's own
// documented residual, first closed by sg2's "self-healing driver
// exemption" in resolveForcedFlushCut, which task tg2 replaced with the
// leak-free-cut search; the test keeps its sg2 name and pins the shape
// under the new criterion). It is an independent reconstruction of task
// sg2's "CONFIRMED REPRO 2 of 2" (the independent reviewer's construction),
// built from the ask task's annotation text alone and verified against the
// PRE-fix code before trusting it (see this task's own self-review): the
// reconstruction produced the exact pre-fix outcome the annotation
// reported (kMax=7, naive cut=299993, splitting a G occurrence at
// [299991,299995)) before this fix landed, and this test now pins the
// fixed behaviour.
//
// Shapes: unit ("RMNOP", 5 bytes) drives a dense, self-overlapping filler
// chain once tracked as its own doubled form c = unit+unit (10 bytes,
// matching at every 5-byte-aligned offset -- overlapping continuously, so
// protectedCrossing can never find a gap anywhere in c's own coverage: the
// "driver" this escape hatch cannot avoid splitting no matter the cut). g
// ("MNOP", 4 bytes) is a SEPARATE tracked secret nested at a fixed phase
// (offset 1) inside every occurrence of unit -- unlike c, g's own
// occurrences are NOT gapless (each leaves the 1 byte at the start of the
// next unit uncovered, e.g. the "R" between two "MNOP"s), so a safe cut
// exists between any two of them. h is the literal last 7 bytes of a
// 300000-byte c-periodic filler plus one distinguishing byte (a PROPER
// prefix only, so it never itself matches anything) -- engineered so
// longestKeepBackForForcedFlush's own byte-suffix cut (kMax=7) retreats
// past the MOST RECENT g occurrence into the one before it, which (before
// this fix) split it: front half "MN" swept into the blanket [redacted]
// marker, back half "OP" left raw in the retained tail with no complete
// "MNOP" left to recognise and redact it by.
func TestValuesFlushPointDriverExemptionResolvesEmbeddedFormRegression(t *testing.T) {
	t.Parallel()
	v := &Values{}
	unit := "RMNOP"
	c := unit + unit // 10 bytes: gapless, self-overlapping driver
	g := "MNOP"      // 4 bytes: nested at offset 1 of every unit
	v.Add([]byte(c))
	v.Add([]byte(g))

	fillerLen := 300000
	filler := strings.Repeat(unit, fillerLen/len(unit)+1)[:fillerLen]
	last7 := filler[len(filler)-7:]
	h := last7 + "X" // 8 bytes: proper prefix only, engineered to retreat kMax past the most recent g
	v.Add([]byte(h))

	s := filler
	if len(s) <= flushStallCap {
		t.Fatalf("test setup: len(s) = %d, want > flushStallCap (%d)", len(s), flushStallCap)
	}

	// Pin the exact pre-fix numbers task sg2's own independent
	// reconstruction confirmed, so this test still catches a regression
	// even if some unrelated future change happens to stop this exact
	// shape from leaking for a different reason.
	if kMax := v.longestKeepBackForForcedFlush(s); kMax != 7 {
		t.Fatalf("test setup: longestKeepBackForForcedFlush = %d, want 7 (task sg2 annotation's own stated value)", kMax)
	}
	if naiveCut := len(s) - v.longestKeepBackForForcedFlush(s); naiveCut != 299993 {
		t.Fatalf("test setup: naive kMax-derived cut = %d, want 299993 (the confirmed pre-fix leak's cut)", naiveCut)
	}

	var gSpans [][2]int
	for from := 0; from < len(s); {
		i := strings.Index(s[from:], g)
		if i < 0 {
			break
		}
		start := from + i
		gSpans = append(gSpans, [2]int{start, start + len(g)})
		from = start + 1
	}

	out, consumed := v.FlushPoint(s)
	if out != Redacted {
		t.Fatalf("FlushPoint = (%q, %d), want the single opaque marker %q as out", out, consumed, Redacted)
	}
	for _, sp := range gSpans {
		if sp[0] < consumed && consumed < sp[1] {
			t.Fatalf("cut=%d splits g's occurrence at [%d,%d) (task sg2 / task 1g2 round 5's residual)", consumed, sp[0], sp[1])
		}
	}

	if consumed < len(s) {
		tail := s[consumed:]
		red := v.Redact(tail)
		// Every "MN" or "OP" substring in this filler's alphabet can only
		// ever occur as part of a g occurrence ("R","M","N","O","P" with g
		// = "MNOP"): a complete one redacts cleanly to Redacted, so any
		// raw "MN" or "OP" surviving Redact can only be a split fragment
		// of one that did not.
		for _, frag := range []string{"MN", "OP"} {
			if strings.Contains(red, frag) {
				t.Fatalf("retained tail %q, Redact(tail) = %q: leaked raw fragment %q of a split g occurrence", tail, red, frag)
			}
		}
	}
}

// TestValuesFlushPointDriverExemptionMechanismRepro1 is a permanent
// regression test for task sg2's "CONFIRMED REPRO 1 of 2" from the task's
// own annotation, reconstructed independently. Unlike repro 2 above, the
// annotation itself is explicit that this construction "happens to land
// safely in THIS specific numeric setup" -- it illustrates the same
// underlying mechanism (an unrelated tracked form H's own byte-suffix
// figure retreating the forced-flush cut by more than the naive
// longest-1 keep-back would) without actually splitting anything in this
// particular alignment. This test's own independent reconstruction
// confirms that same "no leak" outcome (both before and after the
// resolveForcedFlushCut fix), and pins it so a future change cannot
// silently start leaking on this shape either.
func TestValuesFlushPointDriverExemptionMechanismRepro1(t *testing.T) {
	t.Parallel()
	v := &Values{}
	fillerForm := "AAAA"
	g := "db-password-42" // 14 bytes
	gap := strings.Repeat("A", 50)
	v.Add([]byte(fillerForm))
	v.Add([]byte(g))

	filler := strings.Repeat("A", 300000)
	s := filler + g + gap
	// h = the literal last 58 bytes of s, plus one extra byte, so it stays
	// a PROPER prefix (never itself matches).
	last58 := s[len(s)-58:]
	h := last58 + "B"
	v.Add([]byte(h))

	if len(s) <= flushStallCap {
		t.Fatalf("test setup: len(s) = %d, want > flushStallCap (%d)", len(s), flushStallCap)
	}

	out, consumed := v.FlushPoint(s)
	if out != Redacted {
		t.Fatalf("FlushPoint = (%q, %d), want the single opaque marker %q as out", out, consumed, Redacted)
	}
	if consumed < len(s) {
		tail := s[consumed:]
		red := v.Redact(tail)
		if strings.Contains(red, "db-password-42") {
			t.Fatalf("retained tail %q leaked the raw secret unredacted: Redact(tail) = %q", tail, red)
		}
	}
}

// TestLongestPrefixSuffixOverlap unit-tests longestPrefixSuffixOverlap (the
// KMP failure-function helper longestKeepBackForForcedFlush relies on)
// directly: the length of the longest PROPER prefix of form that is also a
// suffix of s. Each case also cross-checks against a naive O(n^2)
// reference implementation, since this exact kind of off-by-one has
// repeatedly been the root cause of leaks in this file's history.
func TestLongestPrefixSuffixOverlap(t *testing.T) {
	cases := []struct {
		name string
		form string
		s    string
		want int
	}{
		{"no overlap", "abcdef", "xyzxyz", 0},
		{"form longer than s, no overlap", "abcdefgh", "xyz", 0},
		{"exact proper-prefix suffix match", "abcdef", "xxxabcde", 5}, // "abcde" (5 of 6 bytes, the max proper prefix) is a suffix of s
		{"only 1-byte overlap", "abcdef", "xxxxxa", 1},
		{"s shorter than form's max prefix", "abcdefgh", "cde", 0},        // "cde" doesn't match any prefix of form
		{"s shorter than form's max prefix, matches", "cdefgh", "xxc", 1}, // form starts with "c"; s ends with "c"
		{"empty form", "", "abc", 0},
		{"single-byte form", "a", "xyz", 0},                                   // maxK = len(form)-1 = 0
		{"periodic self-overlap", "x1x1x1x1x1", strings.Repeat("x1", 100), 8}, // longest proper prefix (9 bytes "x1x1x1x1x") is NOT a suffix due to parity; 8 bytes "x1x1x1x1" is
		{"full buffer equals form's proper prefix", "abcdefgh", "abcdefg", 7}, // s IS exactly form's 7-byte proper prefix
		{"s exactly one byte", "abcdef", "a", 1},
		{"s exactly one byte, no match", "abcdef", "z", 0},
		{"repeated pattern, full overlap up to maxK", "AAAA", strings.Repeat("A", 50), 3}, // maxK=3, "AAA" is a suffix
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := longestPrefixSuffixOverlap(tc.form, tc.s)
			if got != tc.want {
				t.Fatalf("longestPrefixSuffixOverlap(%q, %q) = %d, want %d", tc.form, tc.s, got, tc.want)
			}
			// Cross-check against a naive O(n^2) reference implementation.
			want := naiveLongestPrefixSuffixOverlap(tc.form, tc.s)
			if got != want {
				t.Fatalf("longestPrefixSuffixOverlap(%q, %q) = %d, disagrees with naive reference %d", tc.form, tc.s, got, want)
			}
		})
	}
}

// naiveLongestPrefixSuffixOverlap is longestPrefixSuffixOverlap's O(n^2)
// reference: try every k from the largest possible down to 1 and check
// directly with strings.HasSuffix. Used only to cross-check the KMP-based
// implementation in tests, never in the production path.
func naiveLongestPrefixSuffixOverlap(form, s string) int {
	maxK := len(form) - 1
	if maxK > len(s) {
		maxK = len(s)
	}
	for k := maxK; k >= 1; k-- {
		if strings.HasSuffix(s, form[:k]) {
			return k
		}
	}
	return 0
}

// TestLongestPrefixSuffixOverlapRandomized cross-checks the KMP-based
// implementation against the naive O(n^2) reference over many small random
// inputs, to catch any off-by-one the hand-derived cases above might not
// happen to exercise.
func TestLongestPrefixSuffixOverlapRandomized(t *testing.T) {
	alphabet := "abAB"
	gen := func(seed, n int) string {
		b := make([]byte, n)
		x := seed*2654435761 + 1
		for i := range b {
			x = x*1103515245 + 12345
			b[i] = alphabet[(x>>16)&3]
		}
		return string(b)
	}
	for seed := 0; seed < 500; seed++ {
		formLen := 1 + seed%12
		sLen := seed % 20
		form := gen(seed, formLen)
		s := gen(seed+9999, sLen)
		got := longestPrefixSuffixOverlap(form, s)
		want := naiveLongestPrefixSuffixOverlap(form, s)
		if got != want {
			t.Fatalf("seed %d: longestPrefixSuffixOverlap(%q, %q) = %d, want %d (naive)", seed, form, s, got, want)
		}
	}
}

// formOccurrencesIndexOracle is formOccurrences as it was before task 0h2:
// strings.Index restarted one byte past every occurrence. It is slow for a
// densely self-overlapping form but obviously complete, so the one-pass
// scans formOccurrences now uses are checked against it.
func formOccurrencesIndexOracle(form, s string) [][2]int {
	var spans [][2]int
	for from := 0; from < len(s); {
		i := strings.Index(s[from:], form)
		if i < 0 {
			break
		}
		start := from + i
		spans = append(spans, [2]int{start, start + len(form)})
		from = start + 1
	}
	return spans
}

// TestFormOccurrencesMatchesIndexOracleRandomized is task 0h2's differential
// test: formOccurrences (strings.Index while occurrences are disjoint, KMP
// from the first overlapping one) must return exactly the old Index loop's
// occurrences.
// Small alphabets and periodic forms and streams make borders, overlaps and
// near-misses common; the empty form and empty s are included too.
func TestFormOccurrencesMatchesIndexOracleRandomized(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(0x0b2, 0x5ec))
	alphabets := []string{"a", "ab", "abc", "abAB=-"}
	for iter := range 20000 {
		alphabet := alphabets[iter%len(alphabets)]
		maxForm, maxS := 12, 200
		if iter%100 == 0 {
			maxForm, maxS = 300, 4096
		}
		form := randomText(rng, alphabet, rng.IntN(maxForm+1))
		if iter%3 == 0 && form != "" {
			// A periodic form: a short unit repeated, cut at any length.
			unit := form[:1+rng.IntN(min(len(form), 4))]
			form = strings.Repeat(unit, len(form)/len(unit)+1)[:len(form)]
		}
		s := randomText(rng, alphabet, rng.IntN(maxS+1))
		if iter%2 == 0 && form != "" {
			// A stream mostly made of the form's own pieces, with noise.
			var b strings.Builder
			for b.Len() < len(s) {
				if rng.IntN(4) == 0 {
					b.WriteString(randomText(rng, alphabet, 1+rng.IntN(3)))
				} else {
					b.WriteString(form[rng.IntN(len(form)):])
					b.WriteString(form)
				}
			}
			s = b.String()
		}
		got, want := formOccurrences(form, s), formOccurrencesIndexOracle(form, s)
		if !slices.Equal(got, want) {
			t.Fatalf("iter %d: formOccurrences(%q, %q) = %v, want %v (Index oracle)", iter, form, s, got, want)
		}
	}
}

// TestValuesFlushPointNestedFormsTileDriverUnitRegression is the permanent
// regression test for task tg2 (the residual task sg2's own review
// documented): driver "RMNOP"+"RMNOP" with nested "MNOP" ([5k+1,5k+5)) and
// "PRMN" ([5k+4,5k+8)) tiling its unit, so every cut splits some occurrence.
// For every buffer phase and every keep-back an engineered proper-prefix
// form h can force (k trailing bytes), the forced flush must retain no raw
// byte: the buffer ends at a unit boundary, so every retained byte lies in
// a driver occurrence and any letter left after Redact is a split fragment.
// Phases 2 and 3 are left out: their leading "NO"/"O" belongs to no
// occurrence, so the ordinary path flushes it instead of the escape hatch.
// Before tg2, phases 1 and 4 leaked for 9 of the 13 keep-backs (e.g. k=8
// retained "NOPRMNOP", whose "NO" survived Redact, and k=0 retained
// "RMNOP", a split driver's raw "R"), and phase 0 consumed a single byte.
// It also pins the retained tail below 2*longest (26 bytes, h being 13
// long at k=12), the progress bound resolveForcedFlushCut proves.
func TestValuesFlushPointNestedFormsTileDriverUnitRegression(t *testing.T) {
	t.Parallel()
	unit := "RMNOP"
	for _, phase := range []int{0, 1, 4} {
		for k := 0; k <= 12; k++ {
			s := strings.Repeat(unit, 300000/len(unit)+2)[phase:]
			v := &Values{}
			v.Add([]byte(unit + unit))
			v.Add([]byte("MNOP"))
			v.Add([]byte("PRMN"))
			if k > 0 {
				v.Add([]byte(s[len(s)-k:] + "X"))
			}
			out, consumed := v.FlushPoint(s)
			if out != Redacted || consumed <= 0 {
				t.Fatalf("phase=%d k=%d: FlushPoint = (%q, %d), want a forced flush", phase, k, out, consumed)
			}
			tail := s[consumed:]
			if red := strings.ReplaceAll(v.Redact(tail), Redacted, ""); strings.ContainsAny(red, unit) {
				t.Errorf("phase=%d k=%d: retained tail %.30q redacts to %.30q: a split occurrence leaked", phase, k, tail, red)
			}
			if len(tail) >= 2*13 {
				t.Errorf("phase=%d k=%d: retained %d bytes, want < 2*longest (26)", phase, k, len(tail))
			}
		}
	}
}

// leakFreeCutViolation is the brute-force oracle for resolveForcedFlushCut's
// criterion: it returns a retained byte p >= c that belongs to an
// occurrence starting before c yet to no occurrence lying entirely in
// [c, sLen), or -1 when there is none (c is leak-free).
func leakFreeCutViolation(spans [][2]int, sLen, c int) int {
	split := make([]bool, sLen)
	covered := make([]bool, sLen)
	for _, sp := range spans {
		for p := max(sp[0], c); p < sp[1]; p++ {
			if sp[0] < c {
				split[p] = true
			} else {
				covered[p] = true
			}
		}
	}
	for p := c; p < sLen; p++ {
		if split[p] && !covered[p] {
			return p
		}
	}
	return -1
}

// TestResolveForcedFlushCutLeakFreeRandomized checks resolveForcedFlushCut
// against the brute-force oracle over many random dense buffers: periodic
// text over a small alphabet with sparse mutations, one to four tracked
// forms cut from it (so they nest, overlap and tile each other), and an
// optional proper-prefix form of the buffer's own tail to move the
// keep-back cut kMaxCut. Every result must be leak-free and lie in
// (kMaxCut-longest, kMaxCut], the progress bound resolveForcedFlushCut's
// doc proves.
func TestResolveForcedFlushCutLeakFreeRandomized(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(20260924, 7))
	for trial := range 4000 {
		unit := randomText(rng, "ABC", 2+rng.IntN(6))
		s := []byte(strings.Repeat(unit, 400/len(unit)+1)[:400])
		for range rng.IntN(4) {
			s[rng.IntN(len(s))] = "ABC"[rng.IntN(3)]
		}
		v := &Values{}
		for range 1 + rng.IntN(4) {
			n := 4 + rng.IntN(12)
			at := rng.IntN(len(s) - n)
			v.Add(s[at : at+n])
		}
		if rng.IntN(2) == 0 {
			v.Add(append(append([]byte(nil), s[len(s)-1-rng.IntN(14):]...), 'Z'))
		}
		spans := v.protectedSpans(string(s))
		longest := 0
		for _, sp := range spans {
			longest = max(longest, sp[1]-sp[0])
		}
		kMaxCut := len(s) - v.longestKeepBackForForcedFlush(string(s))
		c := v.resolveForcedFlushCut(string(s), spans)
		if c > kMaxCut || (c != kMaxCut && c <= kMaxCut-longest) {
			t.Fatalf("trial %d: cut %d outside (kMaxCut-longest, kMaxCut] = (%d, %d]", trial, c, kMaxCut-longest, kMaxCut)
		}
		if p := leakFreeCutViolation(spans, len(s), c); p >= 0 {
			t.Fatalf("trial %d: cut %d leaks byte %d of s=%q (spans %v)", trial, c, p, s, spans)
		}
	}
}

// randomText returns n bytes drawn uniformly from alphabet.
func randomText(rng *rand.Rand, alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.IntN(len(alphabet))]
	}
	return string(b)
}

// TestValuesFlushPointTilingShapesRelayLeakFree is task tg2's adversarial
// end-to-end check: forced-flush floods through simulateRelay whose tracked
// forms tile a periodic unit between them without any one short form
// covering it -- three nested forms, a tiling with no driver at all,
// overlapping tilings of different lengths, and a driver plus a longer
// form. What survives outside the Redacted markers must be a subsequence of
// the stream's bytes that lie in no occurrence at all (uncoveredBytes; for
// these whole-unit streams only a few bytes at the very start), so any
// other surviving letter is a leak. Pending must stay within the
// historical-shapes test's invariant-B bound.
func TestValuesFlushPointTilingShapesRelayLeakFree(t *testing.T) {
	t.Parallel()
	shapes := []struct {
		name, unit string
		forms      []string
	}{
		{"three-nested-forms", "ABCDEFG", []string{"ABCDEFGABCDEFG", "BCDE", "EFGA", "GABC"}},
		{"tiling-without-driver", "RMNOP", []string{"MNOP", "PRMN"}},
		{"overlapping-tilings", "ABCDEFGH", []string{"BCDEF", "EFGHAB", "HABCD", "CDEFGHA"}},
		{"driver-plus-longer-form", "RMNOP", []string{"RMNOPRMNOP", "MNOP", "PRMN", "NOPRMNOPRMNOPRM"}},
	}
	for _, sh := range shapes {
		for _, chunk := range []int{32 << 10, 32771} {
			v := &Values{}
			stream := strings.Repeat(sh.unit, (1<<20)/len(sh.unit))
			// A proper-prefix form matching the stream's own tail
			// retreats every forced cut into the tiling.
			forms := append([]string{stream[len(stream)-11:] + "z"}, sh.forms...)
			longest := 0
			for _, f := range forms {
				v.Add([]byte(f))
				longest = max(longest, len(f))
			}
			forwarded, maxPending := simulateRelay(v, []byte(stream), chunk)
			raw := strings.ReplaceAll(forwarded, Redacted, "")
			if free := uncoveredBytes(v, stream); !isSubsequence(raw, free) {
				t.Fatalf("%s/chunk=%d: raw output %.40q is not a subsequence of the uncovered bytes %.40q: a fragment leaked", sh.name, chunk, raw, free)
			}
			if bound := flushStallCap + longest + chunk; maxPending > bound {
				t.Fatalf("%s/chunk=%d: pending reached %d, want <= %d", sh.name, chunk, maxPending, bound)
			}
		}
	}
}

// uncoveredBytes returns, in order, the bytes of s that lie in no occurrence
// of any protected form: the only bytes a leak-free relay may forward raw.
func uncoveredBytes(v *Values, s string) string {
	covered := make([]bool, len(s))
	for _, sp := range v.protectedSpans(s) {
		for p := sp[0]; p < sp[1]; p++ {
			covered[p] = true
		}
	}
	var b strings.Builder
	for p := range len(s) {
		if !covered[p] {
			b.WriteByte(s[p])
		}
	}
	return b.String()
}

// isSubsequence reports whether sub can be obtained from s by deleting bytes.
func isSubsequence(sub, s string) bool {
	i := 0
	for j := 0; i < len(sub) && j < len(s); j++ {
		if sub[i] == s[j] {
			i++
		}
	}
	return i == len(sub)
}

// A secret quoted with %q (as errors quote line content) is redacted even
// when %q and JSON escape it differently.
func TestRedactGoQuotedSecret(t *testing.T) {
	var v Values
	v.Add([]byte("Tr0ub<4dor\"3\n"))
	got := v.Redact(fmt.Sprintf("line %q is owned", "pw=Tr0ub<4dor\"3"))
	if strings.Contains(got, "Tr0ub") {
		t.Fatalf("secret leaked: %s", got)
	}
}

// Redacting a secret read with its trailing newline keeps the line break
// after it.
func TestRedactKeepsNewlineAfterSecret(t *testing.T) {
	var v Values
	v.Add([]byte("hunter22!\n"))
	got := v.Redact("password is hunter22!\nnext line\n")
	if strings.Contains(got, "hunter22") || !strings.Contains(got, "\nnext line") {
		t.Fatalf("redacted = %q", got)
	}
}
