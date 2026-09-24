package seal

import (
	"bytes"
	"time"
)

// This file is the signed-at line of the GONF-SIGNED-PLAN/1 envelope
// (sign.go, task 7g2; docs/plan-signing.md "Replay and rollback"):
//
//	signed-at 2026-09-24T10:50:51Z\n
//
// The timestamp is RFC 3339 restricted to one spelling: UTC, a literal
// upper-case "Z" (never a numeric offset), second precision (no fraction)
// and a four-digit year, so it is always exactly 20 characters. Parsing
// accepts only a line that formats back to itself byte for byte, so no two
// spellings of the same instant (and no out-of-range field such as month
// 13 or second 60) are ever accepted. The line is covered by the envelope
// signature (signedMessage), so its value is authenticated once Verify
// succeeds; whether it is fresh enough is the caller's policy.

// signedAtPrefix opens the signed-at line; signedAtLayout is its
// timestamp's time layout (a lone trailing "Z" is a literal, not a zone
// token, in Go's layout syntax).
const (
	signedAtPrefix = "signed-at "
	signedAtLayout = "2006-01-02T15:04:05Z"
)

// signedAtLineLen is the signed-at line's exact length, newline included.
const signedAtLineLen = len(signedAtPrefix) + len(signedAtLayout) + 1

// formatSignedAt renders at as the canonical timestamp: converted to UTC
// and truncated to the second. It refuses (ErrSignedAtInvalid) the zero
// time, which only an uninitialized clock yields, and a year that would
// not format as exactly four digits.
func formatSignedAt(at time.Time) (string, error) {
	if at.IsZero() {
		return "", ErrSignedAtInvalid
	}
	stamp := at.UTC().Truncate(time.Second).Format(signedAtLayout)
	if len(stamp) != len(signedAtLayout) {
		return "", ErrSignedAtInvalid
	}
	return stamp, nil
}

// appendSignedAtLine appends the signed-at line for stamp (a
// formatSignedAt result) to b.
func appendSignedAtLine(b []byte, stamp string) []byte {
	b = append(b, signedAtPrefix...)
	b = append(b, stamp...)
	return append(b, '\n')
}

// cutSignedAtLine parses b's first line as a signed-at line and returns
// its timestamp text, its value (UTC) and what follows the line. ok is
// false for a missing or wrong prefix, a missing newline (a truncated
// envelope), a line of any other length, or any timestamp that is not the
// one canonical spelling (see the file comment), including the zero time
// formatSignedAt never produces.
func cutSignedAtLine(b []byte) (stamp string, at time.Time, rest []byte, ok bool) {
	if len(b) < signedAtLineLen || b[signedAtLineLen-1] != '\n' {
		return "", time.Time{}, nil, false
	}
	text, found := bytes.CutPrefix(b[:signedAtLineLen-1], []byte(signedAtPrefix))
	if !found {
		return "", time.Time{}, nil, false
	}
	stamp = string(text)
	at, err := time.Parse(signedAtLayout, stamp)
	if err != nil || at.Format(signedAtLayout) != stamp || at.IsZero() {
		return "", time.Time{}, nil, false
	}
	return stamp, at, b[signedAtLineLen:], true
}
