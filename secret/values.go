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
// across two forced flushes of a relayed unterminated line, and the single
// source of the relay's own flush window: MaxPending reports it to
// logger.RedactingWriter, which buffers an unterminated line up to whatever
// MaxPending returns rather than carrying a separate, independently-sized
// threshold of its own, so the two can never drift apart. FlushPoint also
// uses it as the threshold past which it must stop waiting for a
// self-overlapping match chain to resolve and flush what it has (see
// FlushPoint), since every real caller only asks it to find a cut point
// once its own buffer already exceeds this many bytes (guaranteed by
// MaxPending being the relay's flush threshold).
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

// FlushPoint returns the already-redacted text a relay may forward now
// (out) and how many leading bytes of s that consumed (consumed), when s is
// the start of output that continues later (see logger.RedactingWriter): an
// occurrence of the longest tracked form could still start in the last
// len-1 bytes, so those stay pending, and the cut moves back to the start of
// whichever merged run of overlapping matches crosses it, so no secret is
// split between two redactions. Matches are merged in one linear,
// start-position-sorted pass (mergeSpans) rather than chased backward one
// overlapping match at a time: a secret whose repeat period is shorter than
// its own length (e.g. "x1x1x1x1x1", which also matches itself shifted by 2
// bytes, at offsets 0, 2, 4, ...) chains arbitrarily many overlapping
// occurrences together, and walking backward from one match to the next,
// rescanning every span each step, is both quadratic and — because the chain
// reaches all the way to offset 0 — never terminates above 0; a long run of
// such a pattern would then never be forwarded and the caller's pending
// buffer would grow without bound.
//
// When even the merged run's start is 0 (the chain reaches the very
// beginning of s) and s already exceeds MaxSplitGuard bytes — the threshold
// MaxPending reports, which logger.RedactingWriter uses to decide a line is
// overlong enough to force a flush, so every real call here already has
// len(s) that large — moving the cut back to the run's start (<= 0) would
// mean no flush at all, forever: the escape hatch this guards against
// reintroducing. It must NOT instead cut at the run's raw end (run[1]) as an
// earlier, buggy version of this function did: run[1] can fall past the
// len(s)-longest+1 keep-back, and a DIFFERENT, LONGER tracked form can start
// inside this very run (e.g. a periodic S = "x1x1x1x1x1" whose bytes are
// also the prefix of a longer L = S+tail) — matchSpans never reports that
// still-incomplete occurrence, so cutting at run[1] would forward a prefix
// that silently swallows L's leading bytes, and L's tail, arriving on a
// later call with its matching prefix already gone, would then be forwarded
// raw. So the escape hatch keeps the ordinary keep-back cut
// (len(s)-longest+1) as its starting point, preserving the same boundedness
// (every forced flush still drains close to that many bytes) without ever
// losing the last longest-1 bytes a longer, straddling secret might still
// need -- but that starting cut is only a byte offset inside the crossing
// run, not necessarily an occurrence boundary, and the retained tail after
// it is raw secret material: forwarding it unredacted on a later call would
// leak whatever bytes of a real occurrence it began with (Redact can never
// match a partial occurrence). So before returning, the cut is checked
// against protectedCrossing, which finds the largest point at or before it
// that no occurrence of a protected form (one FlushPoint's keep-back is
// actually sized to guard, i.e. no longer than MaxSplitGuard -- see below)
// reaches strictly across. Snapping to the nearest single occurrence's own
// start is NOT enough: a different, still-open occurrence from another
// tracked form can cross that exact point too (e.g. a 10-byte protected
// form at [0,10) and a 5-byte one at [3,8) both cross a cut of 6; snapping
// only to the second form's start, 3, leaves the first form's tail, bytes
// [8,10), sitting raw at the front of the retained text once the
// still-open first occurrence is later completed elsewhere or simply
// forwarded as-is). protectedCrossing instead finds a point that clears
// every protected occurrence, not just the nearest one's start. When it
// finds such a point above 0, the cut snaps back to it, which can only
// shorten the flushed prefix (never lengthen it past the keep-back), so the
// same boundedness holds; progress per flush drops by at most one
// occurrence length instead of by nothing. When it reports that some
// protected occurrence still crosses cut but no safe point exists (a chain
// that stays unresolved for its entire self-overlapping length, e.g.
// "x1x1x1x1x1" repeated with no break anywhere in s -- see
// protectedCrossing), FlushPoint makes NO progress at all this call
// (consumed 0) rather than ever retain a raw fragment: a stall is always
// safe (the caller keeps buffering and retries once more data crosses
// MaxSplitGuard again -- the same "stay conservative" answer already used
// below the threshold), while forwarding any fragment of a protected
// occurrence never is. Only when nothing protected crosses cut at all --
// the crossing is caused solely by a form longer than MaxSplitGuard, which
// the keep-back was never sized to protect in the first place (see below)
// -- does the plain keep-back cut stand as computed, its long-accepted
// trade-off unchanged by any of this. Because the crossing run starts at
// 0, the whole flushed prefix [0, cut) -- using whatever cut is finally
// returned -- is run material with nothing legitimate ahead of it, so it
// is redacted as a single opaque Redacted marker instead of respanned:
// respanning only the prefix (not the full run, which continues past cut)
// can leave a few dangling, non-periodic-aligned bytes at the very end
// unmatched (matches must be complete within the substring given to
// Redact), and always redacting the whole flushed span is the safe side of
// that trade-off.
// Outside the escape hatch, cutting at run[0] (the normal non-crossing-at-0
// case) never has this problem: ordinary Redact, respanning the flushed
// prefix itself, always produces the same single marker for it, since no
// match found over the full s can straddle a cut that sits exactly at a
// (disjoint, sorted) run boundary.
//
// Forms longer than MaxSplitGuard are left out of the keep-back (they are
// still redacted wherever a flushed chunk holds them whole), so a huge
// secret cannot make the relay buffer without bound; protectedCrossing
// leaves their occurrences out of its safety check for the same reason --
// protecting them was never promised, so refusing to make progress on
// their account would defeat the point of excluding them. It implements
// logger.Redactor with Redact and MaxPending.
func (v *Values) FlushPoint(s string) (out string, consumed int) {
	longest := 0
	for _, e := range v.snapshot() {
		if len(e.form) <= MaxSplitGuard {
			longest = max(longest, len(e.form))
		}
	}
	cut := len(s) - max(longest-1, 0)
	if cut <= 0 {
		return "", 0
	}
	spans, _ := v.matchSpans(s, false)
	for _, run := range mergeSpans(spans) {
		if run[0] >= cut || cut >= run[1] {
			continue
		}
		// The merged runs are disjoint and sorted, so at most one can cross
		// cut: no earlier run's end can reach past run[0] (it would have
		// merged into this one), and no later run's start can reach back to
		// or before run[1] for the same reason. Either branch below is
		// therefore final; no further scan or backward step is needed.
		if run[0] <= 0 && len(s) > MaxSplitGuard {
			snap, crossed := protectedCrossing(v.protectedSpans(s), cut)
			switch {
			case snap > 0:
				cut = snap
			case crossed:
				return "", 0
			}
			return Redacted, cut
		}
		cut = run[0]
		break
	}
	if cut <= 0 {
		return "", 0
	}
	return v.Redact(s[:cut]), cut
}

// protectedSpans returns the occurrences, sorted by start, of every tracked
// form no longer than MaxSplitGuard in s: the ones FlushPoint's keep-back is
// actually sized to protect (see FlushPoint's doc). It is a deliberately
// separate, independent scan from matchSpans (some duplicated work, not
// reused via a shared slice) rather than a filter applied after the fact:
// matchSpans' result is sorted in place as a side effect of the mergeSpans
// call FlushPoint already made on it, and a second, differently-filtered
// view built by reordering or re-tagging that same backing array would risk
// exactly the kind of subtle span/metadata desync this file has already
// been burned by (see this function's own history). A fresh, isolated scan
// has no such risk.
func (v *Values) protectedSpans(s string) [][2]int {
	var spans [][2]int
	for _, e := range v.snapshot() {
		if !e.contained || len(e.form) > MaxSplitGuard {
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
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	return spans
}

// protectedCrossing scans spans (sorted by start, as protectedSpans returns
// them) and returns the largest point at or before cut that no span reaches
// strictly across (snap; 0 when no such point exists beyond the trivially
// safe start of s), and whether some span starts before cut and still ends
// after it (crossed; false whenever a safe snap was found, including snap
// == cut itself). A point p is safe exactly when no span [a,b) in spans has
// a < p < b -- not merely when p equals some individual span's own start,
// which does not by itself clear every OTHER span that might still be open
// at p (see FlushPoint's doc for the two-form counter-example this guards
// against). frontier tracks the running end of every span seen so far;
// whenever the next span's start sp[0] has reached or passed frontier, no
// span already processed reaches past frontier (their max end is exactly
// frontier) and no later span (all of which start no earlier than sp, by
// sort order) can start before frontier either, so every point in the gap
// [frontier, sp[0]] is confirmed safe -- not just frontier itself, the gap's
// near edge, but every point up to sp[0], its far edge (an earlier version
// of this function returned only frontier here, needlessly under-reporting
// how far it is actually safe to go, which cannot leak but can make a
// large, entirely ordinary multi-line secret -- e.g. a cert bundle, whose
// own body lines are separately tracked as short protected forms scattered
// through it, see strongLines -- advance only a handful of bytes per call
// despite most of the gap between them being genuinely open). The largest
// point in that gap at or below cut is taken as the new candidate; once a
// span starts at or past cut, the gap already reaches cut itself, so cut is
// the answer and the scan stops (a span starting at or past cut is
// otherwise irrelevant to cut's safety: it begins inside or after the
// retained tail, not before the flushed prefix). If frontier ever exceeds
// cut, nothing further can help: cut sits inside that span's occurrence, so
// the scan stops there too.
//
// When FlushPoint returns "no progress" because no safe point exists at all
// (see its doc), the caller's pending buffer keeps growing and
// protectedSpans/protectedCrossing rescan it from scratch on every later
// call, since nothing was ever forwarded to shrink it — cost proportional
// to len(s), repeated on every still-unresolved call, so total cost grows
// roughly with the square of how long a genuinely never-breaking chain
// persists. This is the accepted cost of never leaking rather than a new
// hang (each individual call still terminates quickly): it can only arise
// when literally no gap exists anywhere in the buffer given so far (a
// perfectly, densely self-overlapping run), which the fix above already
// finds and exploits every gap to avoid whenever one exists.
func protectedCrossing(spans [][2]int, cut int) (snap int, crossed bool) {
	frontier := 0
	for _, sp := range spans {
		if frontier > cut {
			break
		}
		if sp[0] >= frontier {
			if sp[0] >= cut {
				return cut, false
			}
			snap = sp[0]
		}
		frontier = max(frontier, sp[1])
	}
	if frontier <= cut {
		return cut, false
	}
	return snap, true
}

// MaxPending implements logger.Redactor: RedactingWriter must not force a
// flush before an unterminated line reaches MaxSplitGuard bytes, because
// FlushPoint's escape hatch (see above) only has a safe, progress-making
// answer once len(s) already exceeds it; forcing sooner would return "", 0
// forever for a self-overlapping secret and reintroduce the unbounded
// buffer growth task mb2 fixed.
func (v *Values) MaxPending() int { return MaxSplitGuard }

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
	merged := mergeSpans(spans)
	if len(merged) == 0 {
		return s
	}
	var out strings.Builder
	last := 0
	for _, run := range merged {
		out.WriteString(s[last:run[0]])
		out.WriteString(Redacted)
		last = run[1]
	}
	out.WriteString(s[last:])
	return out.String()
}

// mergeSpans sorts spans (byte ranges, start inclusive, end exclusive) by
// start and merges every run of overlapping or touching ones into one
// [start, end) span, so every byte covered by any input span is covered by
// exactly one output span and the output is sorted and disjoint. It is the
// one place that combines matchSpans' possibly-overlapping occurrences into
// maximal runs, used both to replace them (replaceSpans) and to find the run
// crossing a candidate cut point (FlushPoint) — the latter in one linear
// pass instead of FlushPoint's former backward walk that rescanned every
// span for each step and, for a self-overlapping secret, never terminated
// above offset 0. It sorts spans in place; the caller must not reuse the
// slice.
func mergeSpans(spans [][2]int) [][2]int {
	if len(spans) == 0 {
		return nil
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	merged := make([][2]int, 0, len(spans))
	start, end := spans[0][0], spans[0][1]
	for _, sp := range spans[1:] {
		if sp[0] <= end {
			end = max(end, sp[1])
			continue
		}
		merged = append(merged, [2]int{start, end})
		start, end = sp[0], sp[1]
	}
	return append(merged, [2]int{start, end})
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
