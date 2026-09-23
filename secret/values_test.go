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

// Once the buffer exceeds MaxSplitGuard, the same self-overlapping run must
// still be flushed: the old backward-chaining walk moved the cut back one
// overlapping match at a time and, for a periodic secret, that chain always
// reaches offset 0, so FlushPoint returned 0 forever and the caller's
// pending buffer (logger.RedactingWriter, bounded by maxPendingLine, the
// same 64 KiB as MaxSplitGuard) grew without bound. The escape hatch fixing
// that must flush the run once s is already this large, and — this is the
// part task 3d2 fixed after a regression — it must keep the ordinary
// longest-1 keep-back rather than sweeping the run's raw end into the
// flush: the trailing tail below is appended directly (no filler gap), so
// the run genuinely crosses the ordinary cut point and this test actually
// exercises the escape-hatch branch (a prior version of this test placed
// a non-matching gap before the tail, so the cut always landed past the
// run and the branch was never entered — see task 5d2).
func TestValuesFlushPointSelfOverlappingAboveBoundIsFlushedAndRedacted(t *testing.T) {
	t.Parallel()
	var v Values
	v.Add([]byte("x1x1x1x1x1"))                         // 10 bytes: keep 9 back
	run := strings.Repeat("x1", (MaxSplitGuard/2)+4096) // > MaxSplitGuard bytes, all one chain
	tail := "x1x1x1"                                    // more of the same pattern: the chain runs right up to (and past) the cut
	s := run + tail

	out, cut := v.FlushPoint(s)
	if cut == 0 {
		t.Fatal("FlushPoint stayed 0 above MaxSplitGuard: the buffer would grow without bound")
	}
	if out != Redacted {
		t.Fatalf("the escape hatch must redact the flushed prefix as one opaque marker, got %q", out)
	}
	// At least the last 9 bytes (longest-1) must stay pending: the ordinary
	// keep-back, not the run's raw (further) end.
	if kept := len(s) - cut; kept < 9 {
		t.Fatalf("only %d bytes kept back, want at least 9 (longest-1)", kept)
	}
	if cut <= 0 || cut > len(s) {
		t.Fatalf("cut %d out of range for len(s) = %d", cut, len(s))
	}
	if strings.Contains(out, "x1x1x1x1x1") {
		t.Fatalf("raw secret survived in the flushed marker: %q", out)
	}
}

// TestValuesFlushPointTwoSecretLeakRegression is a permanent regression test
// for the vulnerability the review found (task 3d2, a regression against
// e87ca0a~1): a shorter, periodic secret S ("x1x1x1x1x1", period 2) chains
// into one giant self-overlapping merged run, and a second, longer secret
// L = S+tail starts with S's exact bytes. The buggy escape hatch returned
// the merged run's raw end (run[1]) instead of the ordinary keep-back cut,
// so once the buffer crossed MaxSplitGuard the WHOLE periodic run —
// including the bytes that are also L's still-incomplete prefix — was
// flushed with nothing held back. When L's tail then arrived on a later
// write, it no longer had its matching prefix available to complete the
// match against, so it was forwarded completely unredacted: a real,
// exploitable leak of L's tail (this is exactly the shape a relayed child
// process's chunked output goes through — see
// internal/logger.RedactingWriter and its own
// TestRedactingWriterBoundsTwoSecretLeak, the end-to-end version of this
// same scenario). The fix keeps the ordinary longest-1 keep-back even on
// the escape-hatch path, so L's prefix bytes stay pending here and are
// available to complete the match once its tail arrives.
func TestValuesFlushPointTwoSecretLeakRegression(t *testing.T) {
	t.Parallel()
	var v Values
	s1 := "x1x1x1x1x1"                 // period 2, 10 bytes
	s2 := s1 + strings.Repeat("Q", 30) // 40 bytes, starts with s1
	v.Add([]byte(s1))
	v.Add([]byte(s2))

	// The buffer at the moment a real relay's forced flush fires: past
	// MaxSplitGuard, still pure s1-periodic text (s2's tail has not been
	// written yet).
	buf := strings.Repeat("x1", 40000) // 80000 bytes, > MaxSplitGuard

	out, consumed := v.FlushPoint(buf)
	if consumed == 0 {
		t.Fatal("FlushPoint made no progress above MaxSplitGuard: the buffer would grow without bound (the mb2 regression)")
	}
	if out != Redacted {
		t.Fatalf("the escape hatch must redact the flushed prefix as one opaque marker, got %q", out)
	}
	// s2 is the longest tracked form (40 bytes): its 39-byte keep-back must
	// survive, or its prefix is gone before its tail ever arrives.
	if kept := len(buf) - consumed; kept < len(s2)-1 {
		t.Fatalf("only %d bytes kept back, want at least %d (len(s2)-1)", kept, len(s2)-1)
	}

	// s2's tail arrives on a later write; the kept-back bytes plus the tail
	// must still let Redact find and hide the complete secret — the whole
	// point of keeping them pending instead of sweeping them into the
	// escape hatch's flush.
	pending := buf[consumed:] + strings.Repeat("Q", 30) + "\n"
	redactedTail := v.Redact(pending)
	if strings.Contains(redactedTail, strings.Repeat("Q", 30)) {
		t.Fatalf("raw secret tail leaked: %q", redactedTail)
	}
	if !strings.Contains(redactedTail, Redacted) {
		t.Fatalf("the completed secret was not redacted at all: %q", redactedTail)
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
