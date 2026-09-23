package secret

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
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
// version). This exact buffer (pure S-periodic text, L's tail not yet
// written) is ALSO an unbroken self-overlapping chain with no safe
// occurrence-boundary cut anywhere in it yet (see
// TestValuesFlushPointSelfOverlappingAboveBoundStallsRatherThanLeak): under
// the current (task rd2) contract FlushPoint makes no progress on it at all
// rather than guess at any cut, plain or run[1]-based — a strictly stronger
// guarantee than 3d2's original fix (which still returned a plain,
// non-occurrence-aware cut here). The real point this test pins either way:
// L's prefix bytes are never swept away, so once L's tail actually arrives,
// Redact still finds and hides the complete secret.
func TestValuesFlushPointTwoSecretLeakRegression(t *testing.T) {
	t.Parallel()
	var v Values
	s1 := "x1x1x1x1x1"                 // period 2, 10 bytes
	s2 := s1 + strings.Repeat("Q", 30) // 40 bytes, starts with s1
	v.Add([]byte(s1))
	v.Add([]byte(s2))

	// The buffer at the moment a real relay's forced flush fires: past
	// MaxSplitGuard, still pure s1-periodic text (s2's tail has not been
	// written yet) — an unbroken self-overlapping chain, so FlushPoint must
	// make no progress on it yet rather than guess at an unsafe cut.
	buf := strings.Repeat("x1", 40000) // 80000 bytes, > MaxSplitGuard

	out, consumed := v.FlushPoint(buf)
	if consumed != 0 || out != "" {
		t.Fatalf("FlushPoint = (%q, %d), want (\"\", 0): an unbroken self-overlapping chain has no safe cut, so s2's prefix must stay fully pending rather than risk a partial cut", out, consumed)
	}

	// s2's tail arrives on a later write; the whole buffer (nothing was
	// flushed) plus the tail must still let Redact find and hide the
	// complete secret — the whole point of never sweeping any of it away.
	pending := buf + strings.Repeat("Q", 30) + "\n"
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
