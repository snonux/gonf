package secret

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// MinContainedLen is the shortest secret Values.Contains looks for inside a
// larger payload, measured on the value with surrounding whitespace
// trimmed. Every form of a shorter secret (its raw bytes with a newline, its
// JSON escaping) is only recognised when a payload equals it exactly:
// searching for a 1-3 byte string inside plan payloads would mark nearly
// every op as secret material, and redacting it would garble ordinary text.
const MinContainedLen = 4

// MinStrongLen is the shortest secret (trimmed) that can be strong, the
// class Values.ContainsStrong reports. A shorter secret — "paul", "root",
// "git" — is too likely an ordinary word for its presence in an identity or
// a path to prove a leak, so callers mark and redact on a plain Contains
// match but refuse only on a strong one.
const MinStrongLen = 8

// maxWordLen is the longest secret that still counts as word-like, and so
// weak whatever its length: up to this many bytes of only ASCII letters,
// '-' and '_' ("postgres", "backup-user") names accounts and paths as often
// as it is a password. A strong secret needs MinStrongLen bytes and either
// more than maxWordLen bytes or another character (a digit, punctuation).
const maxWordLen = 12

// MaxSplitGuard is the longest form FlushPoint protects from being split
// across two forced flushes of a relayed unterminated line; it matches the
// relay's 64 KiB flush window (logger.RedactingWriter).
const MaxSplitGuard = 64 << 10

// Redacted replaces every recognised secret occurrence in redacted output.
// It is deliberately not valid base64, so a redacted content_b64 can never
// be mistaken for (or decoded as) real content.
const Redacted = "[redacted]"

// Values is the set of secret values a process has resolved, kept so that
// plan recording can recognise secret material after a recipe turned it
// into an ordinary string (file content, template data, a rendered config).
// It is the sensitivity source of gonf's plans: a provider interface alone
// cannot recover sensitivity once a secret has been concatenated into a
// string, but the bytes themselves can still be found again.
//
// Each added value is tracked in three forms: exactly, with surrounding
// whitespace trimmed, and with trailing CR/LF trimmed, because recipes
// routinely strip a secret file's final newline before using it. Each strong
// line of a multi-line secret (a PEM key body line) is tracked as a
// redact-only form of its own: Redact hides it, Contains ignores it. Contains
// and Redact also look for each form's JSON string escaping, so a value
// inside encoded template data is found as well. A transformation beyond
// that (base64, hashing, case changes, splitting) is not recognised.
//
// Values holds plaintext copies for the rest of the process; Go gives no
// guarantee that memory is zeroed later, and Values does not try. The zero
// value is ready to use and safe for concurrent use.
type Values struct {
	mu sync.Mutex
	// forms maps every tracked form (and its JSON escaping) to its matching
	// mode. A form shared by several secrets gets the strongest mode. The
	// set only grows, except for Reset.
	forms map[string]formMode
	// sorted caches the forms longest first for Redact and Contains; nil
	// after every change, never modified in place once built.
	sorted []formEntry
}

// formMode is how a form is matched, decided on the trimmed secret it
// belongs to: contained forms are searched inside payloads (secret at least
// MinContainedLen long), others only match a whole payload; strong forms
// (see isStrong) are also reported by ContainsStrong and RedactStrong.
// redactOnly forms — the lines of a multi-line secret — are used by Redact,
// RedactStrong and FlushPoint but never by Contains or ContainsStrong: a
// line such as "apiVersion: v1" or a shared certificate line is no evidence
// that an op carries this secret, so it must not mark or refuse one.
type formMode struct {
	contained, strong, redactOnly bool
}

// merge combines the modes of two secrets sharing one form: the strongest
// matching mode, and redact-only only when both are.
func (m formMode) merge(o formMode) formMode {
	return formMode{
		contained:  m.contained || o.contained,
		strong:     m.strong || o.strong,
		redactOnly: m.redactOnly && o.redactOnly,
	}
}

// formEntry is one tracked form and its matching mode.
type formEntry struct {
	form string
	formMode
}

// Add tracks data (see Values for the derived forms). Empty data and empty
// forms are ignored.
func (v *Values) Add(data []byte) {
	if len(data) == 0 {
		return
	}
	raw := string(data)
	// The matching mode comes from the secret itself (trimmed), never from
	// a form: "123\n" or the JSON escaping of a short secret are 4+ bytes
	// long but must not turn a 3-byte secret into a substring pattern.
	trimmed := strings.TrimSpace(raw)
	mode := formMode{contained: len(trimmed) >= MinContainedLen, strong: isStrong(trimmed)}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.forms == nil {
		v.forms = map[string]formMode{}
	}
	// The three whole-secret forms share the trimmed secret's mode (so the
	// TrimRight form of "       ab\n" stays as weak as "ab"); only the lines
	// of a multi-line secret are judged on their own, as redact-only forms.
	for _, form := range []string{raw, trimmed, strings.TrimRight(raw, "\r\n")} {
		v.addForm(form, mode)
	}
	for _, line := range strongLines(trimmed) {
		v.addForm(line, formMode{contained: true, strong: true, redactOnly: true})
	}
	v.sorted = nil
}

// addForm tracks form and its JSON escaping with mode, merged with the mode
// of any secret already sharing it. The caller holds v.mu.
func (v *Values) addForm(form string, mode formMode) {
	if form == "" {
		return
	}
	for _, f := range []string{form, jsonEscaped(form)} {
		if old, ok := v.forms[f]; ok {
			v.forms[f] = old.merge(mode)
		} else {
			v.forms[f] = mode
		}
	}
}

// strongLines returns the strong lines (isStrong, surrounding whitespace
// trimmed) of a multi-line secret such as a PEM private key, or nil for a
// one-line one. Each becomes a redact-only form of its own, so output
// redacted line by line (logger.RedactingWriter) still hides the key body.
// PEM armour lines ("-----BEGIN PRIVATE KEY-----") are skipped: they are
// shared by every key and certificate and hide nothing.
func strongLines(trimmed string) []string {
	if !strings.Contains(trimmed, "\n") {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if isStrong(line) && !isPEMArmour(line) {
			lines = append(lines, line)
		}
	}
	return lines
}

// Reset forgets every tracked value (tests; a process normally keeps them).
func (v *Values) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.forms, v.sorted = nil, nil
}

// Empty reports whether no value is tracked.
func (v *Values) Empty() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.forms) == 0
}

// Contains reports whether payload holds a tracked form: it equals one, or
// contains the form of a secret at least MinContainedLen bytes long.
func (v *Values) Contains(payload []byte) bool {
	return v.match(payload, false)
}

// ContainsStrong is Contains restricted to strong secrets (at least
// MinStrongLen bytes trimmed and not word-like, see maxWordLen): a match is
// then evidence of a leak, not of a coincidence with an ordinary word.
func (v *Values) ContainsStrong(payload []byte) bool {
	return v.match(payload, true)
}

// match is Contains (strongOnly false) and ContainsStrong.
func (v *Values) match(payload []byte, strongOnly bool) bool {
	if len(payload) == 0 {
		return false
	}
	for _, e := range v.snapshot() {
		switch {
		case e.redactOnly, strongOnly && !e.strong:
		case e.contained && bytes.Contains(payload, []byte(e.form)):
			return true
		case !e.contained && string(payload) == e.form:
			return true
		}
	}
	return false
}

// RedactStrong is Redact restricted to strong secrets (ContainsStrong):
// for strings that are ordinary metadata (an owner, a mode, an op kind), in
// which a weak secret is far more likely a coincidence than a leak.
func (v *Values) RedactStrong(s string) string {
	return v.redact(s, true)
}

// Redact returns s with every occurrence of a tracked form replaced by
// Redacted. All occurrences of all forms are found first and overlapping or
// adjacent ones are merged into one span, so two secrets that overlap
// ("abcdef" and "defghi" in "xxabcdefghixx") never leave a part of either
// visible. The forms of a short secret (below MinContainedLen) are replaced
// only when they are the whole string, the same rule Contains applies.
func (v *Values) Redact(s string) string {
	return v.redact(s, false)
}

// redact is Redact (strongOnly false) and RedactStrong.
func (v *Values) redact(s string, strongOnly bool) string {
	spans, whole := v.matchSpans(s, strongOnly)
	if whole {
		return Redacted
	}
	return replaceSpans(s, spans)
}

// FlushPoint returns how many leading bytes of s a relay may redact and
// forward now when s is the start of output that continues later (see
// logger.RedactingWriter): an occurrence of the longest tracked form could
// still start in the last len-1 bytes, so those stay, and the cut moves back
// to the start of any occurrence that crosses it, so no secret is split
// between two redactions. Forms longer than MaxSplitGuard are left out of
// the keep-back (they are still redacted wherever a flushed chunk holds
// them whole), so a huge secret cannot make the relay buffer without
// bound. It implements logger.Redactor with Redact.
func (v *Values) FlushPoint(s string) int {
	longest := 0
	for _, e := range v.snapshot() {
		if len(e.form) <= MaxSplitGuard {
			longest = max(longest, len(e.form))
		}
	}
	cut := len(s) - max(longest-1, 0)
	if cut <= 0 {
		return 0
	}
	spans, _ := v.matchSpans(s, false)
	for moved := true; moved; {
		moved = false
		for _, sp := range spans {
			if sp[0] < cut && cut < sp[1] {
				cut, moved = sp[0], true
			}
		}
	}
	return cut
}

// matchSpans returns the byte ranges of every occurrence of every contained
// form in s (strong ones only with strongOnly), overlaps included, and
// whether s as a whole equals a whole-payload (short) form.
func (v *Values) matchSpans(s string, strongOnly bool) (spans [][2]int, whole bool) {
	for _, e := range v.snapshot() {
		if strongOnly && !e.strong {
			continue
		}
		if !e.contained {
			whole = whole || s == e.form
			continue
		}
		for from := 0; from < len(s); {
			i := strings.Index(s[from:], e.form)
			if i < 0 {
				break
			}
			start := from + i
			spans = append(spans, [2]int{start, start + len(e.form)})
			from = start + 1
		}
	}
	return spans, whole
}

// isPEMArmour reports whether line is a PEM "-----BEGIN ...-----" or
// "-----END ...-----" boundary.
func isPEMArmour(line string) bool {
	return strings.HasSuffix(line, "-----") &&
		(strings.HasPrefix(line, "-----BEGIN ") || strings.HasPrefix(line, "-----END "))
}

// isStrong reports whether a trimmed secret is strong (MinStrongLen,
// maxWordLen).
func isStrong(trimmed string) bool {
	if len(trimmed) < MinStrongLen {
		return false
	}
	if len(trimmed) > maxWordLen {
		return true
	}
	for _, r := range trimmed {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return true
		}
	}
	return false
}

// replaceSpans replaces the union of spans (byte ranges of s) by Redacted,
// one marker per merged run.
func replaceSpans(s string, spans [][2]int) string {
	if len(spans) == 0 {
		return s
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	var out strings.Builder
	last := 0
	for i := 0; i < len(spans); {
		start, end := spans[i][0], spans[i][1]
		for i++; i < len(spans) && spans[i][0] <= end; i++ {
			end = max(end, spans[i][1])
		}
		out.WriteString(s[last:start])
		out.WriteString(Redacted)
		last = end
	}
	out.WriteString(s[last:])
	return out.String()
}

// snapshot returns the tracked forms longest first. The slice is rebuilt
// after every Add, never modified in place, so callers may read it without
// the lock.
func (v *Values) snapshot() []formEntry {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sorted == nil && len(v.forms) != 0 {
		sorted := make([]formEntry, 0, len(v.forms))
		for form, mode := range v.forms {
			sorted = append(sorted, formEntry{form: form, formMode: mode})
		}
		slices.SortFunc(sorted, func(a, b formEntry) int {
			if len(a.form) != len(b.form) {
				return len(b.form) - len(a.form)
			}
			return strings.Compare(a.form, b.form)
		})
		v.sorted = sorted
	}
	return v.sorted
}

// jsonEscaped returns form as it appears inside a JSON string literal
// (without the quotes), with the escaping encoding/json produces. It is how
// a secret placed into template data appears on the template_data wire
// field and in any JSON-encoded plan line.
func jsonEscaped(form string) string {
	raw, err := json.Marshal(form)
	if err != nil || len(raw) < 2 {
		return form
	}
	return string(raw[1 : len(raw)-1])
}
