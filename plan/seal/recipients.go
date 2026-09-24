package seal

import (
	"errors"
	"fmt"
	"strings"

	"filippo.io/age"
)

// ErrRecipientRefused marks a line ParseRecipients refuses because it names
// a recipient type other than age1pq: a classic X25519 age1 recipient, an
// ssh-* recipient, or anything else (including an age plugin recipient),
// per the package-level "Recipient policy" doc.
var ErrRecipientRefused = errors.New("recipient refused: gonf accepts only age1pq (hybrid) recipients")

// ErrRecipientMalformed marks a line that looks like an age1pq recipient
// (the right prefix) but fails to parse as one.
var ErrRecipientMalformed = errors.New("malformed age1pq recipient")

// Recipient is a validated age1pq hybrid recipient, as returned by
// ParseRecipients and accepted by Seal. The zero value is not valid.
type Recipient struct {
	inner *age.HybridRecipient
}

// ParseRecipients validates lines as age1pq hybrid recipients, one per
// entry, labeling any refusal with the generic "recipient line %d" (see
// ParseRecipientsFrom for a caller that needs a more specific origin, e.g.
// one of several merged sources). A blank entry (after trimming, see
// below) or one starting with "#" is ignored, matching the age
// recipients-file convention (one recipient per line, "#" comments), so a
// caller can pass either the lines of a recipients file or the values of
// repeated -recipient flags through the same function.
//
// Each entry is trimmed of leading/trailing whitespace (including a
// trailing "\r" from a CRLF-terminated recipients file) before
// classification: age's own recipient parse error is deliberately
// discarded (see parseHybridRecipient), so an untrimmed "\r" or trailing
// space produced an opaque "malformed age1pq recipient" with nothing
// pointing at the real, mundane cause — a recipients file saved by a
// Windows editor, not a malicious or corrupted key (task de2).
//
// Any entry that is not an age1pq recipient is refused with
// ErrRecipientRefused, naming the entry's 1-based line number and its class
// (classic X25519, ssh, unrecognized/plugin) but never its content — see
// the package doc's "Recipient policy". An entry that has the age1pq prefix
// but fails to parse (a corrupted or truncated key) is refused with
// ErrRecipientMalformed, also without echoing it.
//
// ParseRecipients does not itself refuse an empty result (no non-comment
// lines): a caller unions recipients from several sources (an operator
// recipients file, -recipient flags, a per-host recipient) before sealing,
// and only Seal, at the point it would otherwise produce an unreadable
// artifact, refuses zero recipients.
func ParseRecipients(lines []string) ([]Recipient, error) {
	return parseRecipients(lines, recipientLineLabel)
}

// ParseRecipientsFrom is ParseRecipients for a single, already-identified
// source: label(i) names the 0-based index i's origin (e.g. "-recipient
// #2" for a repeated flag, or "<path>:2" for a recipients file's own
// 1-based line number) instead of ParseRecipients' generic "recipient line
// %d".
//
// A caller that merges several sources into one slice before validating —
// as internal/cli/plan_seal.go's resolvePlanRecipients used to, unioning
// -recipient flags with a recipients file's lines — reports a refused or
// malformed entry's index over the MERGED slice, which is not the index an
// operator can find anything at: two -recipient flags ahead of a bad file
// line 2 produced "recipient line 4", and a file opened at line 4 has
// nothing wrong with it (task de2). Calling ParseRecipientsFrom once per
// source, each with its own label, reports the entry's true origin
// regardless of how the caller went on to merge the results.
//
// A nil label falls back to ParseRecipients' generic "recipient line %d"
// (task jg2). Calling a nil label used to panic, and only on a refused or
// malformed entry, never on an all-valid or comment-only input: an
// embedder passing nil would ship and then crash the first time a
// recipients file happened to hold, say, a classic age1 line. A panic on
// input is forbidden by the error-handling contract (docs/design/plan.md), so the
// nil case is defined rather than documented as a precondition.
func ParseRecipientsFrom(lines []string, label func(i int) string) ([]Recipient, error) {
	if label == nil {
		label = recipientLineLabel
	}
	return parseRecipients(lines, label)
}

// String returns the recipient's age1pq… encoding. Unlike an Identity, a
// Recipient is a public key and safe to log or display.
func (r Recipient) String() string {
	if r.inner == nil {
		return ""
	}
	return r.inner.String()
}

// recipientLineLabel is ParseRecipients' default label: the entry's
// 1-based line number, phrased as "recipient line %d" for a single,
// undifferentiated source.
func recipientLineLabel(i int) string {
	return fmt.Sprintf("recipient line %d", i+1)
}

// parseRecipients is the shared validation loop behind ParseRecipients and
// ParseRecipientsFrom; label(i) formats the 0-based index i of a refused or
// malformed entry into the text an error names it by.
func parseRecipients(lines []string, label func(i int) string) ([]Recipient, error) {
	var out []Recipient
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := parseRecipientLine(line)
		if err != nil {
			return nil, fmt.Errorf("plan/seal: %s: %w", label(i), err)
		}
		out = append(out, Recipient{inner: r})
	}
	return out, nil
}

// parseRecipientLine classifies and validates one non-comment,
// non-blank recipient line.
func parseRecipientLine(line string) (*age.HybridRecipient, error) {
	switch {
	case strings.HasPrefix(line, "age1pq1"):
		return parseHybridRecipient(line)
	case strings.HasPrefix(line, "ssh-"):
		return nil, fmt.Errorf("%w: ssh recipient; generate a hybrid key with age-keygen -pq", ErrRecipientRefused)
	case strings.HasPrefix(line, "age1"):
		return nil, classicOrPluginRecipientError(line)
	default:
		return nil, fmt.Errorf("%w: unrecognized recipient type; generate a hybrid key with age-keygen -pq", ErrRecipientRefused)
	}
}

// parseHybridRecipient parses line, already known to have the age1pq1
// prefix, as a hybrid recipient. A parse failure is reported without
// including line: it is public-key material that a copy/paste mistake may
// have corrupted, but the package's blanket rule (never echo recipient or
// identity content) is simpler to keep than to special-case.
func parseHybridRecipient(line string) (*age.HybridRecipient, error) {
	r, err := age.ParseHybridRecipient(line)
	if err != nil {
		return nil, ErrRecipientMalformed
	}
	return r, nil
}

// classicOrPluginRecipientError distinguishes a classic X25519 (age1…)
// recipient from an age plugin recipient (also age1-prefixed, e.g.
// age1yubikey1…) so the refusal names the right class, without echoing
// line: age's own ParseX25519Recipient success or failure is enough to
// tell them apart, and its error text (which would otherwise repeat line)
// is discarded either way.
func classicOrPluginRecipientError(line string) error {
	if _, err := age.ParseX25519Recipient(line); err == nil {
		return fmt.Errorf("%w: classic X25519 (age1) recipient; generate a hybrid key with age-keygen -pq", ErrRecipientRefused)
	}
	return fmt.Errorf("%w: unrecognized age1-prefixed recipient, possibly a plugin; generate a hybrid key with age-keygen -pq", ErrRecipientRefused)
}

// toAgeRecipients adapts recipients to the age.Recipient slice age.Encrypt
// takes. Every member is a *age.HybridRecipient (ParseRecipients and
// ParseRecipientsFrom, which shares its validation loop, are the only
// constructors of Recipient), so every label age.Encrypt sees is
// "postquantum" (age.HybridRecipient.WrapWithLabels): recipients coming
// through this package can never trigger age's own "incompatible
// recipients" mixing refusal, because they are never mixed with a classic
// recipient in the first place.
func toAgeRecipients(recipients []Recipient) []age.Recipient {
	out := make([]age.Recipient, len(recipients))
	for i, r := range recipients {
		out[i] = r.inner
	}
	return out
}
