package plan

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snonux/gonf/plan/seal"
)

// keyTestOps is a small plan with one blob-backed file op.
func keyTestOps() []Op {
	return []Op{
		{Op: KindPlan, Version: CurrentVersion, ID: "k"},
		{Op: KindFile, Path: "/tmp/k", Mode: "0600", Blob: "blobs/k.txt"},
	}
}

// keyTestKey generates an ephemeral identity and returns it as a PushKey,
// with its recipient and raw line (for leak checks).
func keyTestKey(t *testing.T) (PushKey, seal.Recipient, string) {
	t.Helper()
	id, rcpt, err := seal.GenerateEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	line, err := seal.EncodeEphemeral(id)
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewPushKey(line)
	if err != nil {
		t.Fatalf("NewPushKey(EncodeEphemeral line): %v", err)
	}
	return key, rcpt, line
}

// keyTestFrame returns a GONF-PUSH/2 frame with one file blob, the
// recipient matching its key, and the raw key line.
func keyTestFrame(t *testing.T) ([]byte, seal.Recipient, string) {
	t.Helper()
	key, rcpt, line := keyTestKey(t)
	mem := NewMemoryStore()
	if _, err := mem.WriteFile("k.txt", []byte("blob-bytes")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := EncodePushWithKey(&buf, keyTestOps(), mem, key); err != nil {
		t.Fatalf("EncodePushWithKey: %v", err)
	}
	return buf.Bytes(), rcpt, line
}

// The /2 frame is exactly the /1 frame with the magic bumped and one key
// line inserted after it; DecodePushWithKey returns the same ops and blobs
// and a key that, parsed by plan/seal, opens what the recipient sealed.
func TestEncodeDecodePushWithKeyRoundTrip(t *testing.T) {
	frame, rcpt, line := keyTestFrame(t)
	if want := pushMagicV2 + "\n" + pushKeyPrefix + line + "\nblobs 1\n"; !bytes.HasPrefix(frame, []byte(want)) {
		t.Fatalf("frame does not start with the /2 magic, key line and blobs line")
	}
	dir := t.TempDir()
	payload, err := DecodePushWithKey(bytes.NewReader(frame), dir)
	if err != nil {
		t.Fatalf("DecodePushWithKey: %v", err)
	}
	if payload.Key == nil || len(payload.Ops) != 2 || payload.PlanDir != dir {
		t.Fatalf("payload = %+v, want key, 2 ops and plan dir", payload)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "blobs", "k.txt")); err != nil || string(got) != "blob-bytes" {
		t.Fatalf("blob = %q, %v", got, err)
	}
	id, err := seal.ParseEphemeral(payload.Key.Line())
	if err != nil {
		t.Fatalf("decoded key does not parse: %v", err)
	}
	var sealed bytes.Buffer
	w, err := seal.Seal(&sealed, []seal.Recipient{rcpt})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "secret")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := seal.Open(&sealed, []seal.Identity{id})
	if err != nil {
		t.Fatalf("decoded key does not open: %v", err)
	}
	if got, _ := io.ReadAll(r); string(got) != "secret" {
		t.Fatalf("opened %q", got)
	}
}

// Old frame, new reader: DecodePushWithKey decodes a GONF-PUSH/1 frame just
// like DecodePush, with no key; and EncodePush still writes that frame.
func TestDecodePushWithKeyAcceptsV1Frame(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodePush(&buf, keyTestOps()[:1], nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte(pushMagic+"\nblobs 0\nplan\n")) {
		t.Fatalf("EncodePush no longer writes the unchanged /1 frame: %q", buf.Bytes()[:20])
	}
	payload, err := DecodePushWithKey(bytes.NewReader(buf.Bytes()), "")
	if err != nil || payload.Key != nil || len(payload.Ops) != 1 {
		t.Fatalf("DecodePushWithKey(/1) = %+v, %v; want 1 op and no key", payload, err)
	}
}

// New frame, reader that does not accept a key: DecodePush (every current
// apply path) refuses a /2 frame with ErrPushKeyNotAccepted before reading
// the key line or extracting any blob, and never echoes the key. A gonf
// that predates /2 compares the magic with "GONF-PUSH/1" exactly, so it
// refuses on the first line, before the key line too.
func TestDecodePushRefusesV2Frame(t *testing.T) {
	frame, _, line := keyTestFrame(t)
	dir := t.TempDir()
	_, err := DecodePush(bytes.NewReader(frame), dir)
	if !errors.Is(err, ErrPushKeyNotAccepted) {
		t.Fatalf("DecodePush(/2) = %v, want ErrPushKeyNotAccepted", err)
	}
	if strings.Contains(err.Error(), line) || strings.Contains(err.Error(), "AGE-SECRET") {
		t.Fatal("refusal leaks the key (error not echoed)")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("refused /2 frame extracted %d entries", len(entries))
	}
	first, _, _ := strings.Cut(string(frame), "\n")
	if first == pushMagic || strings.Contains(first, "AGE-SECRET") {
		t.Fatalf("first line %q would not be refused by a /1-only reader, or holds key bytes", first)
	}
}

// A malformed key line fails with ErrPushKeyLine before any blob is
// extracted, and the error never contains the line's bytes.
func TestDecodePushWithKeyRefusesMalformedKeyLine(t *testing.T) {
	_, _, line := keyTestKey(t)
	cases := map[string]string{
		"missing prefix":   line + "\n",
		"carriage return":  pushKeyPrefix + line + "\r\n",
		"trailing space":   pushKeyPrefix + line + " \n",
		"inner space":      pushKeyPrefix + line[:20] + " " + line[20:] + "\n",
		"no terminator":    pushKeyPrefix + line,
		"too long":         pushKeyPrefix + strings.Repeat("A", maxPushKeyLine) + "\n",
		"empty key":        pushKeyPrefix + "\n",
		"blobs line first": "blobs 0\n",
	}
	for name, keyLine := range cases {
		t.Run(name, func(t *testing.T) {
			frame := pushMagicV2 + "\n" + keyLine + "blobs 1\n"
			dir := t.TempDir()
			_, err := DecodePushWithKey(strings.NewReader(frame), dir)
			if !errors.Is(err, ErrPushKeyLine) {
				t.Fatalf("err = %v, want ErrPushKeyLine", err)
			}
			if strings.Contains(err.Error(), line[20:40]) || strings.Contains(err.Error(), "AGE-SECRET") {
				t.Fatal("error leaks key bytes (error not echoed)")
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("extracted %d entries despite the bad key line", len(entries))
			}
		})
	}
}

// plan checks only the key line's framing; a well-framed but tampered key
// decodes, and plan/seal.ParseEphemeral (the destination's next step)
// refuses it without echoing it.
func TestDecodePushWithKeyTamperedKeyRefusedBySeal(t *testing.T) {
	_, _, line := keyTestKey(t)
	c := byte('Q')
	if line[30] == 'Q' {
		c = 'P'
	}
	tampered := line[:30] + string(c) + line[31:]
	key, err := NewPushKey(tampered)
	if err != nil {
		t.Fatalf("NewPushKey(tampered) = %v, want the framing accepted", err)
	}
	var frame bytes.Buffer
	if err := EncodePushWithKey(&frame, keyTestOps()[:1], nil, key); err != nil {
		t.Fatal(err)
	}
	payload, err := DecodePushWithKey(&frame, "")
	if err != nil {
		t.Fatalf("DecodePushWithKey(tampered) = %v", err)
	}
	_, err = seal.ParseEphemeral(payload.Key.Line())
	if err == nil || strings.Contains(err.Error(), tampered[20:40]) {
		t.Fatalf("ParseEphemeral(tampered) refused=%v, want a refusal without key bytes (error not echoed)", err != nil)
	}
}

// The zero PushKey is refused instead of writing a /2 frame without a key,
// and NewPushKey refuses a value that could break the line.
func TestPushKeyZeroAndFramingRefused(t *testing.T) {
	var buf bytes.Buffer
	err := EncodePushWithKey(&buf, keyTestOps()[:1], nil, PushKey{})
	if !errors.Is(err, ErrPushKeyLine) || buf.Len() != 0 {
		t.Fatalf("EncodePushWithKey(zero) = %v (wrote %d bytes), want ErrPushKeyLine and nothing written", err, buf.Len())
	}
	for _, bad := range []string{"", "a\nb", "a b", "a\x00", strings.Repeat("A", maxPushKeyLine+1)} {
		if _, err := NewPushKey(bad); !errors.Is(err, ErrPushKeyLine) {
			t.Fatalf("NewPushKey(%q) = %v, want ErrPushKeyLine", bad, err)
		}
	}
}

// No fmt verb prints a PushKey, alone or inside a PushPayload.
func TestPushKeyFormatRedacts(t *testing.T) {
	key, _, line := keyTestKey(t)
	payload := PushPayload{Key: &key}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%T %v"} {
		for _, arg := range []any{key, &key, payload, &payload} {
			if out := fmt.Sprintf(verb, arg); strings.Contains(out, line) || strings.Contains(out, "AGE-SECRET") {
				t.Fatalf("fmt %s of %T leaks the key (%d bytes of output, not echoed)", verb, arg, len(out))
			}
		}
	}
	if fmt.Sprint(key) != redactedPushKey {
		t.Fatalf("Sprint(key) = %q", fmt.Sprint(key))
	}
}

// PushHasBlobs finds a /2 frame's blobs line behind its key line.
func TestPushHasBlobsV2Frame(t *testing.T) {
	frame, _, _ := keyTestFrame(t)
	if !PushHasBlobs(frame) {
		t.Fatal("PushHasBlobs(/2 with blobs) = false")
	}
	key, _, _ := keyTestKey(t)
	var buf bytes.Buffer
	if err := EncodePushWithKey(&buf, keyTestOps()[:1], nil, key); err != nil {
		t.Fatal(err)
	}
	if PushHasBlobs(buf.Bytes()) {
		t.Fatal("PushHasBlobs(/2 without blobs) = true")
	}
	if PushHasBlobs([]byte(pushMagicV2 + "\nkey x")) {
		t.Fatal("PushHasBlobs(truncated /2) = true")
	}
}

// readPushKeyLine consumes exactly the key line, leaving the reader at the
// blobs line.
func TestReadPushKeyLineLeavesBlobsLine(t *testing.T) {
	_, _, line := keyTestKey(t)
	br := bufio.NewReader(strings.NewReader(pushKeyPrefix + line + "\nblobs 0\n"))
	if _, err := readPushKeyLine(br); err != nil {
		t.Fatal(err)
	}
	if rest, _ := io.ReadAll(br); string(rest) != "blobs 0\n" {
		t.Fatalf("rest = %q", rest)
	}
}
