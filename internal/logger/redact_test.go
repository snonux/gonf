package logger

import (
	"strings"
	"testing"
)

// The writer redacts complete lines, so a secret split across two writes is
// still caught, and Close forwards the unterminated tail.
func TestRedactingWriterRedactsSplitLines(t *testing.T) {
	SetRedactor(func(s string) string { return strings.ReplaceAll(s, "fake-secret", "[redacted]") })
	t.Cleanup(func() { SetRedactor(nil) })
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

// Log lines and Redact both use the installed redactor; without one they
// are unchanged.
func TestSetRedactorAppliesToLogLines(t *testing.T) {
	output, restore := CaptureForTest(LevelInfo)
	t.Cleanup(restore)
	if got := Redact("fake-secret"); got != "fake-secret" {
		t.Fatalf("Redact without a redactor = %q", got)
	}
	SetRedactor(func(s string) string { return strings.ReplaceAll(s, "fake-secret", "[redacted]") })
	t.Cleanup(func() { SetRedactor(nil) })
	Info("value %s", "fake-secret")
	if strings.Contains(output(), "fake-secret") || !strings.Contains(output(), "value [redacted]") {
		t.Fatalf("log = %q", output())
	}
}
