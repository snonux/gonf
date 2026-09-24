package seal

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestEphemeralRoundTripSealOpen(t *testing.T) {
	id, rcpt, err := GenerateEphemeral()
	if err != nil {
		t.Fatalf("GenerateEphemeral: %v", err)
	}
	line, err := EncodeEphemeral(id)
	if err != nil {
		t.Fatalf("EncodeEphemeral: %v", err)
	}
	parsed, err := ParseEphemeral(line)
	if err != nil {
		t.Fatalf("ParseEphemeral: %v", err)
	}

	// Seal to the recipient generated alongside the ORIGINAL identity, then
	// open with the identity that went through encode -> parse: proves the
	// text form carries the full key, not just something that parses.
	sealed := sealBytes(t, "ephemeral secret payload", []Recipient{rcpt})
	r, err := Open(bytes.NewReader(sealed), []Identity{parsed})
	if err != nil {
		t.Fatalf("Open with parsed identity: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "ephemeral secret payload" {
		t.Fatalf("round trip got %q", got)
	}

	// The recipient is a valid age1pq value that ParseRecipients accepts,
	// and re-encoding the parsed identity is byte-identical (stable form).
	if _, err := ParseRecipients([]string{rcpt.String()}); err != nil {
		t.Fatalf("ParseRecipients(ephemeral recipient): %v", err)
	}
	again, err := EncodeEphemeral(parsed)
	if err != nil || again != line {
		t.Fatalf("re-encode differs or failed: %v", err)
	}
}

func TestEphemeralWrongIdentityCannotOpen(t *testing.T) {
	_, rcpt, err := GenerateEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := GenerateEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	sealed := sealBytes(t, "x", []Recipient{rcpt})
	if _, err := Open(bytes.NewReader(sealed), []Identity{other}); err == nil {
		t.Fatal("a different ephemeral identity opened the sealed data")
	}
}

func TestGenerateEphemeralNeverRepeats(t *testing.T) {
	seenID := map[string]bool{}
	seenRcpt := map[string]bool{}
	for i := 0; i < 8; i++ {
		id, rcpt, err := GenerateEphemeral()
		if err != nil {
			t.Fatal(err)
		}
		line, err := EncodeEphemeral(id)
		if err != nil {
			t.Fatal(err)
		}
		if seenID[line] || seenRcpt[rcpt.String()] {
			t.Fatalf("iteration %d repeated an earlier ephemeral key", i)
		}
		seenID[line], seenRcpt[rcpt.String()] = true, true
	}
}

func TestEncodeEphemeralIsOneCleanLine(t *testing.T) {
	id, _, err := GenerateEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	line, err := EncodeEphemeral(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "AGE-SECRET-KEY-PQ-1") {
		t.Fatalf("unexpected prefix in encoded identity (len %d)", len(line))
	}
	for i, r := range line {
		if isSpaceOrControl(r) {
			t.Fatalf("encoded line has whitespace/control rune %U at %d", r, i)
		}
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			t.Fatalf("encoded line has unexpected rune %U at %d", r, i)
		}
	}
	if strings.ContainsAny(line, "\r\n") {
		t.Fatal("encoded line contains a line terminator")
	}
}

func TestEncodeEphemeralZeroIdentity(t *testing.T) {
	if _, err := EncodeEphemeral(Identity{}); !errors.Is(err, ErrEphemeralZeroIdentity) {
		t.Fatalf("got %v, want ErrEphemeralZeroIdentity", err)
	}
}

func TestParseEphemeralRefusals(t *testing.T) {
	id, _, err := GenerateEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	good, err := EncodeEphemeral(id)
	if err != nil {
		t.Fatal(err)
	}
	classic := "AGE-SECRET-KEY-1GQ9778VQF9L3WSQ2ZHTVA9A6YC2XHTXYVFT8G7VQAJ0X8A0DX8QSTHV4TK"

	tests := []struct {
		name string
		line string
		want error
	}{
		{"empty", "", ErrIdentityLineFormat},
		{"trailing newline", good + "\n", ErrIdentityLineFormat},
		{"trailing CR", good + "\r", ErrIdentityLineFormat},
		{"leading space", " " + good, ErrIdentityLineFormat},
		{"trailing space", good + " ", ErrIdentityLineFormat},
		{"embedded tab", good[:20] + "\t" + good[20:], ErrIdentityLineFormat},
		{"embedded newline", good[:20] + "\n" + good[20:], ErrIdentityLineFormat},
		{"two lines", good + "\n" + good, ErrIdentityLineFormat},
		{"unicode space", good + " ", ErrIdentityLineFormat},
		{"NUL", good + "\x00", ErrIdentityLineFormat},
		{"truncated", good[:len(good)-10], ErrIdentityMalformed},
		{"tampered", flipOneChar(good), ErrIdentityMalformed},
		{"prefix only", "AGE-SECRET-KEY-PQ-1", ErrIdentityMalformed},
		{"classic identity", classic, ErrIdentityRefused},
		{"garbage", "hello", ErrIdentityRefused},
		{"recipient instead", "age1pq1abcdef", ErrIdentityRefused},
	}
	body := good[len("AGE-SECRET-KEY-PQ-1"):]
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEphemeral(tt.line)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got err %v, want %v", err, tt.want)
			}
			if got.inner != nil {
				t.Fatal("refused line still returned a usable identity")
			}
			// The error must never echo any of the (secret) input; check the
			// distinctive body of the valid key, which every case derives
			// from, as well as the whole line.
			if msg := err.Error(); strings.Contains(msg, body[:16]) || (len(tt.line) > 8 && strings.Contains(msg, tt.line)) {
				t.Fatalf("error leaks input: %q", msg)
			}
		})
	}
}

// TestEphemeralFileImportsNoFilesystem pins the "never touches disk" claim
// by construction: ephemeral.go may import no package that gives it file
// system access.
func TestEphemeralFileImportsNoFilesystem(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "ephemeral.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"os": true, "io/fs": true, "io/ioutil": true, "path": true, "path/filepath": true,
		"syscall": true, "golang.org/x/sys/unix": true, "bufio": true,
		"github.com/snonux/gonf/internal/safepath": true,
	}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if forbidden[path] {
			t.Errorf("ephemeral.go imports %q: it must not touch the file system", path)
		}
	}
}
