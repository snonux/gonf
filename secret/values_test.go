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

// FlushPoint keeps back the bytes the longest form could still start in and
// never cuts through an occurrence.
func TestValuesFlushPoint(t *testing.T) {
	t.Parallel()
	var v Values
	if got := v.FlushPoint("abc"); got != 3 {
		t.Fatalf("FlushPoint without values = %d, want 3", got)
	}
	v.Add([]byte("S3cr3tP@ss")) // 10 bytes: keep 9 back
	s := strings.Repeat("x", 20) + "S3cr"
	if got := v.FlushPoint(s); got != len(s)-9 {
		t.Fatalf("FlushPoint = %d, want %d", got, len(s)-9)
	}
	// A complete occurrence crossing the cut moves the cut to its start.
	s = strings.Repeat("x", 20) + "S3cr3tP@ss" + "yyyy"
	if got := v.FlushPoint(s); got != 20 {
		t.Fatalf("FlushPoint = %d, want 20 (the occurrence start)", got)
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
	if got := v.FlushPoint(s); got != len(s) {
		t.Fatalf("FlushPoint = %d, want %d (huge forms are not split-guarded)", got, len(s))
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
