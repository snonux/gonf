package seal

import (
	"errors"
	"strings"
	"testing"

	"filippo.io/age"
)

func mustHybridRecipientLine(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatalf("generate hybrid identity: %v", err)
	}
	return id.Recipient().String()
}

func mustClassicRecipientLine(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate X25519 identity: %v", err)
	}
	return id.Recipient().String()
}

func TestParseRecipientsAcceptsHybridOnly(t *testing.T) {
	pq1, pq2 := mustHybridRecipientLine(t), mustHybridRecipientLine(t)
	recipients, err := ParseRecipients([]string{
		"# a comment",
		"",
		pq1,
		pq2,
	})
	if err != nil {
		t.Fatalf("ParseRecipients: %v", err)
	}
	if len(recipients) != 2 {
		t.Fatalf("got %d recipients, want 2", len(recipients))
	}
}

func TestParseRecipientsEmptyInputIsNotAnError(t *testing.T) {
	recipients, err := ParseRecipients(nil)
	if err != nil {
		t.Fatalf("ParseRecipients(nil): %v", err)
	}
	if len(recipients) != 0 {
		t.Fatalf("got %d recipients from nil input, want 0", len(recipients))
	}

	recipients, err = ParseRecipients([]string{"# only comments", ""})
	if err != nil {
		t.Fatalf("ParseRecipients(comments only): %v", err)
	}
	if len(recipients) != 0 {
		t.Fatalf("got %d recipients from comments-only input, want 0", len(recipients))
	}
}

func TestParseRecipientsRefusesClassicX25519(t *testing.T) {
	classic := mustClassicRecipientLine(t)
	_, err := ParseRecipients([]string{classic})
	requireRecipientRefusal(t, err, classic, "line 1")
	if !strings.Contains(err.Error(), "classic X25519") {
		t.Fatalf("error does not name the classic X25519 class: %v", err)
	}
}

func TestParseRecipientsRefusesSSH(t *testing.T) {
	const sshLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJlOySfrRIBs+vRfNCwSTJVsSBUXbcqBwLKcyG9ftxfW"
	_, err := ParseRecipients([]string{sshLine})
	requireRecipientRefusal(t, err, sshLine, "line 1")
	if !strings.Contains(err.Error(), "ssh") {
		t.Fatalf("error does not name the ssh class: %v", err)
	}
}

func TestParseRecipientsRefusesPlugin(t *testing.T) {
	// Real age plugin recipients look like age1<plugin-name>1<data...>; a
	// short, syntactically similar but bogus one is enough to exercise the
	// "not X25519, not age1pq" branch without needing a real plugin.
	const pluginLine = "age1yubikey1qwqfayxsu3jwzeh8usx0jgnh9uk443n5ax0duz3s9tqztsxsqhkg9jj"
	_, err := ParseRecipients([]string{pluginLine})
	requireRecipientRefusal(t, err, pluginLine, "line 1")
}

func TestParseRecipientsRefusesUnrecognized(t *testing.T) {
	const garbage = "not-a-recipient-at-all"
	_, err := ParseRecipients([]string{garbage})
	requireRecipientRefusal(t, err, garbage, "line 1")
}

func TestParseRecipientsRefusesMixedList(t *testing.T) {
	pq := mustHybridRecipientLine(t)
	classic := mustClassicRecipientLine(t)

	// The classic line is second: prove the whole call is refused, not just
	// silently filtered down to the valid entries.
	_, err := ParseRecipients([]string{pq, classic})
	requireRecipientRefusal(t, err, classic, "line 2")

	// And the reverse order, since ParseRecipients stops at the first bad
	// line rather than scanning the whole list first.
	_, err = ParseRecipients([]string{classic, pq})
	requireRecipientRefusal(t, err, classic, "line 1")
}

func TestParseRecipientsMalformedHybridLine(t *testing.T) {
	// Right prefix, corrupted payload: still refused, and still without
	// echoing the line.
	pq := mustHybridRecipientLine(t)
	corrupted := flipOneChar(pq)
	_, err := ParseRecipients([]string{corrupted})
	if !errors.Is(err, ErrRecipientMalformed) {
		t.Fatalf("got %v, want ErrRecipientMalformed", err)
	}
	if strings.Contains(err.Error(), corrupted) {
		t.Fatalf("error echoes the corrupted recipient line: %v", err)
	}
}

// TestParseRecipientsTrimsCRLFAndTrailingWhitespace pins task de2's finding
// (c): readRecipientsFileLines (recipients_file.go) splits a recipients
// file only on "\n", so a CRLF-terminated line (or one with trailing
// spaces from a stray paste) used to keep its trailing "\r" or spaces all
// the way into age.ParseHybridRecipient, which fails on that otherwise
// well-formed key — and parseHybridRecipient deliberately discards age's
// own parse error (never echoing recipient content), so the result was an
// opaque "malformed age1pq recipient" with nothing pointing at the real,
// mundane cause. ParseRecipients now trims each line before
// classification, so a CRLF-terminated or trailing-whitespace line that is
// otherwise a valid recipient is accepted.
func TestParseRecipientsTrimsCRLFAndTrailingWhitespace(t *testing.T) {
	pq := mustHybridRecipientLine(t)

	crlf, err := ParseRecipients([]string{pq + "\r"})
	if err != nil {
		t.Fatalf("ParseRecipients(CRLF-terminated line): %v", err)
	}
	if len(crlf) != 1 {
		t.Fatalf("got %d recipients from a CRLF-terminated line, want 1", len(crlf))
	}

	trailingSpace, err := ParseRecipients([]string{pq + "  "})
	if err != nil {
		t.Fatalf("ParseRecipients(trailing-whitespace line): %v", err)
	}
	if len(trailingSpace) != 1 {
		t.Fatalf("got %d recipients from a trailing-whitespace line, want 1", len(trailingSpace))
	}

	leadingSpace, err := ParseRecipients([]string{"  " + pq})
	if err != nil {
		t.Fatalf("ParseRecipients(leading-whitespace line): %v", err)
	}
	if len(leadingSpace) != 1 {
		t.Fatalf("got %d recipients from a leading-whitespace line, want 1", len(leadingSpace))
	}
}

// requireRecipientRefusal asserts err wraps ErrRecipientRefused, mentions
// lineDesc (e.g. "line 1"), and never contains offendingLine's content.
func requireRecipientRefusal(t *testing.T, err error, offendingLine, lineDesc string) {
	t.Helper()
	if err == nil {
		t.Fatal("ParseRecipients accepted a refused recipient type")
	}
	if !errors.Is(err, ErrRecipientRefused) {
		t.Fatalf("got %v, want it to wrap ErrRecipientRefused", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, lineDesc) {
		t.Fatalf("error %q does not name %s", msg, lineDesc)
	}
	if strings.Contains(msg, offendingLine) {
		t.Fatalf("error %q echoes the offending line's content", msg)
	}
}
