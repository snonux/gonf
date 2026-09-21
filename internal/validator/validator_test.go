package validator

import (
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"unicode/utf8"
)

// Unit tests of the runner's pieces (moved from resource/file with the
// runner, task l62/i52). The end-to-end behaviour through File's
// WithValidation stays in resource/file/validation_exec_test.go, and
// run_test.go covers RunIn directly.

// sanitizeValidatorOutput is the whole sanitizing pipeline in one call (a
// test helper): the sanitizeValidatorLines joined with validatorLineSeparator.
func sanitizeValidatorOutput(raw []byte) string {
	return strings.Join(sanitizeValidatorLines(raw), validatorLineSeparator)
}

// sanitizeValidatorOutput keeps printable text, folds lines (LF, CR, CRLF)
// into " | " separated segments, drops blank lines, and replaces control and
// format characters (terminal escapes, NUL, bidi overrides) and invalid UTF-8
// with '?', so validator output cannot forge log lines or terminal state.
func TestSanitizeValidatorOutput(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"\n \n\t\n", ""},
		{"ok\n", "ok"},
		{"a\r\nb\rc\n\nd", "a | b | c | d"},
		{"\x1b[31mred\x1b[0m", "?[31mred?[0m"},
		{"nul\x00byte\ttab", "nul?byte tab"},
		{"bidi\u202eevil", "bidi?evil"},
		{"bad\xff\xfeutf8", "bad?utf8"},
		{"unicode ü ok", "unicode ü ok"},
		{"line\u2028sep\u2029para", "line?sep?para"},
	}
	for _, tt := range tests {
		if got := sanitizeValidatorOutput([]byte(tt.in)); got != tt.want {
			t.Errorf("sanitizeValidatorOutput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// cappedOutput keeps at most its limit across several writes, always reports
// the full write as consumed, and names the total size once it truncated.
func TestCappedOutputKeepsPrefixAndCountsTotal(t *testing.T) {
	out := &cappedOutput{limit: 5}
	for _, chunk := range []string{"abc", "defg", "hij"} {
		if n, err := out.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil", chunk, n, err, len(chunk))
		}
	}
	if len(out.buf) > out.limit {
		t.Fatalf("kept %d raw bytes, want at most %d", len(out.buf), out.limit)
	}
	if got, want := out.String(), "abcde [output truncated, 10 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got := (&cappedOutput{limit: 5}).String(); got != "" {
		t.Fatalf("empty String() = %q, want empty", got)
	}
	short := &cappedOutput{limit: 5}
	_, _ = short.Write([]byte("abc\n"))
	if got := short.String(); got != "abc" {
		t.Fatalf("untruncated String() = %q, want %q", got, "abc")
	}
}

// Kept output that renders empty says so instead of an empty text before a
// truncation note; whitespace-only output with nothing dropped stays "".
func TestCappedOutputWithoutPrintableText(t *testing.T) {
	blank := &cappedOutput{limit: 5}
	_, _ = blank.Write([]byte("     x"))
	if got, want := blank.String(), "[no printable output; 6 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	spaces := &cappedOutput{limit: 5}
	_, _ = spaces.Write([]byte(" \n\t"))
	if got := spaces.String(); got != "" {
		t.Fatalf("whitespace String() = %q, want empty", got)
	}
}

// Sanitizing can grow the kept bytes; the rendered text is cut back to the
// limit and marked truncated even when no raw byte was dropped.
func TestCappedOutputCapsSanitizedText(t *testing.T) {
	out := &cappedOutput{limit: 6}
	_, _ = out.Write([]byte("a\nb\nc\n"))
	if got, want := out.String(), "a | b [output truncated, 6 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// A cut inside a separator does not leave it dangling.
	cut := &cappedOutput{limit: 4}
	_, _ = cut.Write([]byte("a\nb\n"))
	if got, want := cut.String(), "a [output truncated, 4 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// A cut inside a multi-byte rune backs off to the rune start: all 7 raw
	// bytes are kept, "ab | üü" renders to 9 bytes and byte 8 is the middle
	// of the second ü.
	multi := &cappedOutput{limit: 8}
	_, _ = multi.Write([]byte("ab\nüü"))
	got := multi.String()
	text := strings.TrimSuffix(got, " [output truncated, 7 bytes in total]")
	if text != "ab | ü" || !utf8.ValidString(got) || len(text) > multi.limit {
		t.Fatalf("String() = %q, want valid UTF-8 %q within %d bytes", got, "ab | ü", multi.limit)
	}
	// A cut that exposes a space inside a line drops it: "x\nab cd" renders
	// "x | ab cd" (9 bytes); at limit 7 the second line is cut to "ab ".
	inner := &cappedOutput{limit: 7}
	_, _ = inner.Write([]byte("x\nab cd"))
	if got, want := inner.String(), "x | ab [output truncated, 7 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// The validator's own " |" at the cut point is its text, not a separator
	// gonf inserted, so it is kept.
	own := &cappedOutput{limit: 4}
	_, _ = own.Write([]byte("ab |cd"))
	if got, want := own.String(), "ab | [output truncated, 6 bytes in total]"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// processState runs cmd to completion (after start, if given) and returns
// its real ProcessState.
func processState(t *testing.T, cmd *exec.Cmd, afterStart func(*os.Process)) *os.ProcessState {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if afterStart != nil {
		afterStart(cmd.Process)
	}
	_ = cmd.Wait()
	return cmd.ProcessState
}

// validatorTimedOut is only true when our kill was delivered and the
// validator did not exit on its own. The states are real: an exit 0 after a
// "successful" kill (the zombie window), a death by a signal we did not
// send, and a genuine kill.
func TestValidatorTimedOut(t *testing.T) {
	exited := processState(t, exec.Command("sh", "-c", "exit 0"), nil)
	foreign := processState(t, exec.Command("sh", "-c", "kill -USR1 $$"), nil)
	killed := processState(t, exec.Command("sleep", "60"), func(p *os.Process) {
		if err := p.Kill(); err != nil {
			t.Fatal(err)
		}
	})
	tests := []struct {
		name   string
		state  *os.ProcessState
		killed bool
		want   bool
	}{
		{"exited after kill (zombie)", exited, true, false},
		{"foreign signal", foreign, false, false},
		{"killed by us", killed, true, true},
		{"killed by someone else", killed, false, false},
		{"not started", nil, true, false},
	}
	for _, tt := range tests {
		if got := validatorTimedOut(tt.state, tt.killed); got != tt.want {
			t.Errorf("%s: validatorTimedOut = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// killForTimeout records the kill only when it was delivered.
func TestKillForTimeoutRecordsOnlyDeliveredKill(t *testing.T) {
	for _, tt := range []struct {
		killErr error
		want    bool
	}{{nil, true}, {os.ErrProcessDone, false}, {syscall.EPERM, false}} {
		var killed atomic.Bool
		err := killForTimeout(func() error { return tt.killErr }, &killed)
		if err != tt.killErr || killed.Load() != tt.want {
			t.Errorf("kill error %v: returned %v, recorded %v; want %v, %v", tt.killErr, err, killed.Load(), tt.killErr, tt.want)
		}
	}
}

// truncateUTF8 never splits a multi-byte rune.
func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abc", 2, "ab"},
		{"aü", 2, "a"},
		{"aü", 3, "aü"},
		{"ü", 1, ""},
	}
	for _, tt := range tests {
		if got := truncateUTF8(tt.in, tt.n); got != tt.want {
			t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
