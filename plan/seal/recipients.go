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

// String returns the recipient's age1pq… encoding. Unlike an Identity, a
// Recipient is a public key and safe to log or display.
func (r Recipient) String() string {
	if r.inner == nil {
		return ""
	}
	return r.inner.String()
}

// ParseRecipients validates lines as age1pq hybrid recipients, one per
// entry. A blank entry or one starting with "#" is ignored, matching the
// age recipients-file convention (one recipient per line, "#" comments), so
// a caller can pass either the lines of a recipients file or the values of
// repeated -recipient flags through the same function.
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
	var out []Recipient
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := parseRecipientLine(line)
		if err != nil {
			return nil, fmt.Errorf("plan/seal: recipient line %d: %w", i+1, err)
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
// takes. Every member is a *age.HybridRecipient (ParseRecipients is the
// only constructor of Recipient), so every label age.Encrypt sees is
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
