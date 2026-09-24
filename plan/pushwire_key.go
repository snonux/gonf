package plan

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// GONF-PUSH/2 is GONF-PUSH/1 with exactly one extra line, right after the
// magic, carrying a per-push ephemeral age1pq identity (w82 phase 4, task
// zf2; docs/design/plan-encryption.md, "Phase 4 design: sealed multi-chunk
// sticky-dir blobs", "Wire extension"):
//
//	GONF-PUSH/2\n
//	key AGE-SECRET-KEY-PQ-1...\n   (plan/seal.EncodeEphemeral's line)
//	blobs 0\n | blobs 1\n<gzip tar>
//	plan\n<gzip plan JSONL>
//
// It is a clean version bump, not an optional line inside /1: every gonf
// that predates it compares the magic for exact equality with
// "GONF-PUSH/1" and refuses a /2 frame outright ("bad magic"), before
// reading the key line, so an old destination can neither misparse the
// frame nor echo the key. The key line is mandatory in /2: a push that
// needs no key keeps sending a byte-identical /1 frame (EncodePush).
//
// The key sits right after the magic, not after the blobs section, so a
// reader learns whether it holds a key before it extracts anything, and so
// DecodePush can refuse a /2 frame without ever reading key bytes.
//
// plan stays free of cryptography (TestPlanImportsOnlyResourceCore: plan
// depends on no plan/seal): the key travels here as an opaque PushKey. The
// caller turns a seal.Identity into its line with seal.EncodeEphemeral
// before encoding, and parses PushKey.Line with seal.ParseEphemeral after
// decoding, which is also where the key's cryptographic validity is
// checked; this file only enforces the line's framing.
const pushMagicV2 = "GONF-PUSH/2"

// pushKeyPrefix starts a GONF-PUSH/2 frame's key line.
const pushKeyPrefix = "key "

// maxPushKeyLine bounds the key line readPushKeyLine accepts, terminator
// included, and the value NewPushKey accepts. An encoded age1pq identity
// is about 100 bytes; the bound only stops a crafted frame from making the
// reader buffer an unbounded line (bufio.Reader.ReadSlice already stops at
// its 4 KiB buffer; this is the tighter, explicit limit).
const maxPushKeyLine = 512

// ErrPushKeyNotAccepted is returned by DecodePush for a GONF-PUSH/2 frame:
// the frame carries a per-push ephemeral key and sealed sticky-dir refs
// that only DecodePushWithKey's caller can honour. The refusal happens
// before the key line is read, so the error never relates to key bytes.
var ErrPushKeyNotAccepted = errors.New("plan push: GONF-PUSH/2 frame carries an ephemeral key this apply path does not accept")

// ErrPushKeyLine marks a GONF-PUSH/2 key line (or a NewPushKey value) that
// is missing, oversized, not "key <value>", or whose value is empty or
// contains whitespace or control characters. Its message, and every error
// wrapping it, never include the line's content, which is private key
// material.
var ErrPushKeyLine = errors.New("plan push: malformed GONF-PUSH/2 key line")

// PushKey is the opaque per-push ephemeral key a GONF-PUSH/2 frame carries:
// the single-line text of an age1pq identity (plan/seal.EncodeEphemeral).
// It is private key material. Every fmt verb prints a fixed placeholder
// (Format), so a stray %v, %s, %q, %x or %#v of a PushKey or of a
// PushPayload holding one never prints the key; the only way to read it is
// the explicit Line method, for plan/seal.ParseEphemeral.
type PushKey struct {
	line string
}

// redactedPushKey is what every fmt verb prints for a PushKey.
const redactedPushKey = "[ephemeral key redacted]"

// NewPushKey wraps line, one plan/seal.EncodeEphemeral result, as a
// PushKey. It checks only that line fits one frame line (non-empty, at most
// maxPushKeyLine bytes, no whitespace or control characters) and fails
// with ErrPushKeyLine otherwise, never echoing line.
func NewPushKey(line string) (PushKey, error) {
	if line == "" || len(line) > maxPushKeyLine || strings.IndexFunc(line, isKeyLineBreaker) >= 0 {
		return PushKey{}, ErrPushKeyLine
	}
	return PushKey{line: line}, nil
}

// isKeyLineBreaker reports a rune that could split or pad a frame line.
func isKeyLineBreaker(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}

// Line returns the key's text for plan/seal.ParseEphemeral. Never log it,
// write it to disk or put it in an error.
func (k PushKey) Line() string { return k.line }

// Format implements fmt.Formatter so that every verb, %#v included,
// prints redactedPushKey instead of the key.
func (k PushKey) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redactedPushKey)
}

// EncodePushWithKey writes a GONF-PUSH/2 frame to w: EncodePush's frame
// with key on the key line. It is for one elevated chunk that must decrypt
// a sealed sticky-dir ref (internal/remote); the frame travels only on
// that chunk's own stdin, never on argv or in the environment.
//
// The zero PushKey is refused (ErrPushKeyLine) rather than sending a /2
// frame without a usable key; nothing is written then. No error returned
// here includes the key: the only content-bearing write is to w itself.
func EncodePushWithKey(w io.Writer, ops []Op, mem BlobReader, key PushKey) error {
	if key.line == "" {
		return ErrPushKeyLine
	}
	return encodePushFrame(w, pushMagicV2, key.line, ops, mem)
}

// DecodePushWithKey is DecodePush for a caller that can honour a per-push
// ephemeral key: it accepts a GONF-PUSH/1 frame or bare JSONL exactly like
// DecodePush (payload.Key stays nil) and, in addition, a GONF-PUSH/2 frame,
// whose key it returns in payload.Key. A /2 frame whose key line is
// malformed fails with ErrPushKeyLine before any blob is extracted.
//
// Receiving the key is the caller's commitment to use it: a caller that
// cannot decrypt the sealed sticky refs the frame's ops need must use
// DecodePush instead, which refuses the frame.
func DecodePushWithKey(r io.Reader, planDir string) (*PushPayload, error) {
	return decodePush(r, planDir, true)
}

// PushIsKeyed reports whether data, the start of a push stream (a peek is
// enough), begins with the GONF-PUSH/2 magic line: a frame carrying a
// per-push ephemeral key. The apply CLI uses it, with PushHasBlobs, to
// decide how to stage a frame before decoding it; the decoder itself stays
// the authority on whether the frame is well-formed.
func PushIsKeyed(data []byte) bool {
	return bytes.HasPrefix(data, []byte(pushMagicV2+"\n"))
}

// readPushKeyLine reads a GONF-PUSH/2 key line. It strips exactly the "\n"
// terminator and nothing else, so a stray "\r" or space reaches NewPushKey
// and is refused there. Every error is ErrPushKeyLine, optionally wrapping
// an I/O error (which carries no line content).
func readPushKeyLine(br *bufio.Reader) (PushKey, error) {
	raw, err := br.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(raw) > maxPushKeyLine {
		return PushKey{}, fmt.Errorf("%w: line too long", ErrPushKeyLine)
	}
	if err != nil {
		return PushKey{}, fmt.Errorf("%w: %w", ErrPushKeyLine, err)
	}
	line := string(raw[:len(raw)-1])
	value, ok := strings.CutPrefix(line, pushKeyPrefix)
	if !ok {
		return PushKey{}, fmt.Errorf("%w: missing %q prefix", ErrPushKeyLine, pushKeyPrefix)
	}
	return NewPushKey(value)
}
