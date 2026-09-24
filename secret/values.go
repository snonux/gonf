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
// uses it as the threshold past which its escape hatch may run for a
// self-overlapping match chain that reaches back to the very start of the
// buffer (see FlushPoint): below it, FlushPoint either makes ordinary
// keep-back progress or correctly has too little data yet to keep back its
// longest tracked form, never a stall; only above it can the escape hatch's
// own stall-then-cap sequence run, since every real caller only asks it to
// find a cut point once its own buffer already exceeds this many bytes
// (guaranteed by MaxPending being the relay's flush threshold). The escape
// hatch does not itself flush at this threshold — it keeps stalling
// (returning no progress) until flushStallCap, a multiple of this value,
// forces it to (see flushStallCap and FlushPoint's doc; task le2 corrected
// an earlier version of this comment that wrongly implied it flushed here).
const MaxSplitGuard = 64 << 10

// flushStallCap bounds how large s (an already-overlong, still-unterminated
// pending line) may grow while FlushPoint's escape hatch keeps finding no
// safe cut at all (protectedCrossing's crossed result, see FlushPoint):
// once len(s) reaches this cap, FlushPoint stops waiting for a gap that a
// sufficiently dense, self-overlapping chain of protected occurrences may
// never produce, and instead forces progress by recomputing the keep-back
// directly from s's own trailing bytes (longestKeepBackForForcedFlush, see
// FlushPoint's own doc) instead of trusting the naive len(s)-(longest-1)
// figure or reasoning about matched spans at all. Pending after a capped
// flush is therefore bounded by flushStallCap+longest at most (never more:
// longestKeepBackForForcedFlush can only return a value up to longest-1,
// the same margin the ordinary, non-stalled keep-back always allows) -- a
// small, still-bounded increase over the cap alone, not the unbounded
// growth task mb2 fixed.
//
// This restores the bounded-pending, roughly-linear-cost invariant task
// mb2 established: without a cap, a caller such as logger.RedactingWriter
// never shrinks pending on a "", 0 result (see forwardSafePrefix), so
// every later Write re-scans the whole, still-growing buffer -- task rd2's
// own leak fix correctly refused the unsafe guess that used to bound this
// stall, which reintroduced mb2's unbounded growth for this one input
// shape until this cap closed it again (task le2). le2's own cap forced
// progress by consuming the WHOLE buffer unconditionally (cut = len(s)),
// which could sweep away the visible prefix of a DIFFERENT, longer
// tracked form whose completion had not arrived yet (task 1g2 round 1).
// Keeping back the ordinary, unmodified cut instead was ALSO unsafe
// (crossed==true means some occurrence DEFINITELY straddles that exact
// cut, guaranteeing a split -- task 1g2 round 2). Extending forward past
// whichever occurrences happened to look individually "safe" -- first by
// exact-longest-length (round 2's own fix), then by a same-length
// prefix-of-something-longer check applied to whole matched spans (task
// 1g2 round 3) -- fixed those two shapes but still reasoned in terms of
// SPANS and their boundaries, which a fourth construction exploited: a
// different tracked form's forming prefix hiding PARTWAY THROUGH a span
// that looked safe as a whole (task 1g2 round 4, found by this task's own
// hand-derivation immediately after round 3, before it ever shipped).
// longestKeepBackForForcedFlush closes all four by not reasoning about
// spans at all: it asks, independently for every possible trailing-byte
// count, whether s's own suffix could still be the start of ANY tracked
// form's completion, which is the one condition that is actually
// load-bearing (see longestKeepBackForForcedFlush's own doc for the full
// history and worked examples of each prior round's shape). Set well
// above MaxSplitGuard (4x here) so the ordinary, non-degenerate keep-back
// path -- which already keeps pending near MaxSplitGuard between calls --
// never reaches it; only the escape hatch's own stall does, and only for
// an input dense enough to keep failing to find a gap across that many
// bytes.
const flushStallCap = 4 * MaxSplitGuard

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
// (consumed 0) rather than ever retain a raw fragment -- UNLESS s has
// already grown past flushStallCap, in which case it stops stalling and
// recomputes the keep-back directly from s's own trailing bytes instead
// (longestKeepBackForForcedFlush and flushStallCap have the full
// reasoning, including the shapes of every narrower alternative this
// function tried and rejected first): below the cap, a stall is always
// safe (the caller keeps buffering and retries once more data crosses
// MaxSplitGuard again -- the same "stay conservative" answer already used
// below the threshold), while forwarding any of s's trailing bytes that
// could still be the start of a not-yet-complete tracked form never is,
// and above the cap the portion of the buffer beyond that recomputed
// keep-back is treated as entirely secret material rather than left to
// grow the buffer without bound; an
// occurrence still crossing the final cut is left in the retained tail
// exactly as the
// ordinary, non-stalled path would leave it.
// Only when nothing protected crosses cut at all --
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
			byForm := v.protectedFormSpans(s)
			snap, crossed := protectedCrossing(flattenSorted(byForm), cut)
			switch {
			case snap > 0:
				cut = snap
			case crossed && len(s) < flushStallCap:
				// Still short of the cap: keep waiting for a gap
				// rather than guess (see flushStallCap's doc).
				return "", 0
			case crossed:
				// Past the cap with no gap ever found: stop stalling,
				// but do NOT simply consume the whole buffer (task
				// 1g2 round 1: that used to set cut = len(s), which
				// dropped the ordinary keep-back entirely and could
				// strand an in-progress longer occurrence's swept
				// prefix), do NOT just keep cut at its naive,
				// unmodified value either (task 1g2 round 2: crossed
				// == true means BY DEFINITION some occurrence
				// straddles this exact cut, so returning it unchanged
				// guarantees a split), and do NOT extend cut past
				// whole matched SPANS based on their own text alone
				// (task 1g2 round 3, extendPastResolvableOccurrences:
				// found insufficient by an independent review before
				// it ever shipped, because a DIFFERENT tracked form's
				// forming prefix can start PARTWAY THROUGH a span
				// that its own full text made look safe to resolve
				// past). Recompute the keep-back directly from s's own
				// trailing bytes instead of the naive longest-1 figure
				// (longestKeepBackForForcedFlush, via
				// resolveForcedFlushCut): task 1g2 round 5 / task sg2 found
				// that the byte-suffix figure alone can still land inside a
				// SEPARATE, already-complete occurrence of a DIFFERENT
				// tracked form, so resolveForcedFlushCut retreats it further
				// whenever a safe point exists (see that function's own doc
				// for the full mechanism and proof sketch).
				cut = v.resolveForcedFlushCut(s, byForm)
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

// protectedFormSpans returns the occurrences of every tracked form no
// longer than MaxSplitGuard in s, keyed by form: the ones FlushPoint's
// keep-back is actually sized to protect (see FlushPoint's doc). It is a
// deliberately separate, independent scan from matchSpans (some duplicated
// work, not reused via a shared slice) rather than a filter applied after
// the fact: matchSpans' result is sorted in place as a side effect of the
// mergeSpans call FlushPoint already made on it, and a second,
// differently-filtered view built by reordering or re-tagging that same
// backing array would risk exactly the kind of subtle span/metadata desync
// this file has already been burned by (see this function's own history).
// A fresh, isolated scan has no such risk. Keyed by form rather than
// flattened into one slice (as an earlier version of this function, then
// named protectedSpans, returned) so FlushPoint's caller can pass the SAME
// scan to both the ordinary protectedCrossing check (flattenSorted) and
// resolveForcedFlushCut (which needs one form's occurrences in isolation,
// to judge that form on its own rather than mixed into every other tracked
// form's spans) without re-scanning s a second time for the same forms —
// task sg2's own self-review found a second, independent per-form rescan
// here measurably slower under `go test -race`, close enough to
// TestValuesFlushPointHistoricalShapesBoundedAndLeakFree's own generous
// deadline margin to flake past it.
func (v *Values) protectedFormSpans(s string) map[string][][2]int {
	spans := map[string][][2]int{}
	for _, e := range v.snapshot() {
		if !e.contained || len(e.form) > MaxSplitGuard {
			continue
		}
		if occ := formOccurrences(e.form, s); len(occ) > 0 {
			spans[e.form] = occ
		}
	}
	return spans
}

// flattenSorted unions every form's spans in byForm into one slice, sorted
// by start as protectedCrossing requires (protectedFormSpans' doc explains
// why the scan is kept keyed by form instead of flat in the first place).
func flattenSorted(byForm map[string][][2]int) [][2]int {
	var spans [][2]int
	for _, occ := range byForm {
		spans = append(spans, occ...)
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	return spans
}

// formOccurrences returns the byte ranges of every occurrence (overlaps
// included) of form alone in s, in start order (the underlying Index scan
// already produces them left to right). It is the one place that walks a
// single form's occurrences, shared by protectedFormSpans and matchSpans
// (each unions it over every tracked form) -- resolveForcedFlushCut reuses
// protectedFormSpans' own per-form result directly rather than calling this
// again, to judge each form in isolation without a second scan of s.
func formOccurrences(form, s string) [][2]int {
	var spans [][2]int
	for from := 0; from < len(s); {
		i := strings.Index(s[from:], form)
		if i < 0 {
			break
		}
		start := from + i
		spans = append(spans, [2]int{start, start + len(form)})
		from = start + 1
	}
	return spans
}

// protectedCrossing scans spans (sorted by start, as flattenSorted or a
// single form's own occurrences from protectedFormSpans provide them) and
// returns the largest point at or before cut that no span reaches
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
// protectedFormSpans/protectedCrossing rescan it from scratch on every later
// call, since nothing was ever forwarded to shrink it — cost proportional
// to len(s), repeated on every still-unresolved call, so cost would grow
// roughly with the square of how long a genuinely never-breaking chain
// persists if the stall were allowed to continue unbounded. It is not: once
// s reaches flushStallCap, FlushPoint stops calling protectedCrossing
// altogether and redacts the crossing run's flushed prefix, [0, cut), as
// one opaque Redacted marker instead, keeping the same longest-1-byte
// keep-back the ordinary path always retains rather than consuming
// everything (see flushStallCap), so the quadratic-shaped cost is itself
// capped at O(flushStallCap²) before pending drops back near that
// keep-back size and the cost starts over — a bounded, repeating cost
// rather than one that keeps compounding for as long as a pathological
// stream continues (task le2; an earlier version of this comment called
// the unbounded growth "accepted", which task rd2's leak fix had
// reintroduced as a live regression rather than a deliberate trade-off).
// It can only arise when literally no gap exists anywhere in the buffer
// given so far (a perfectly, densely self-overlapping run), which the fix
// above already finds and exploits every gap to avoid whenever one
// exists.
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

// longestKeepBackForForcedFlush returns how many of s's trailing bytes
// FlushPoint's escape hatch must still retain when forced past
// flushStallCap with no safe point anywhere (protectedCrossing's crossed
// result, snap == 0): the length of the longest PROPER prefix of any
// tracked form (no longer than MaxSplitGuard) that is also a suffix of s,
// i.e. the largest k such that some tracked form F has F[:k] == the last k
// bytes of s and k < len(F). That k is exactly how many trailing bytes of
// s could still be the visible start of a not-yet-complete occurrence of
// F: anything shorter is provably NOT the start of F (a mismatch
// somewhere), and F cannot already be complete within those k bytes
// (k < len(F) by construction, and if F had actually matched in full
// somewhere it would already show up as its own complete occurrence,
// handled separately by the ordinary, non-forced path above). Keeping
// back the LARGEST such k over every tracked form -- not just the
// registry's single longest form, and not merely "past cut" spans found
// by matchSpans/protectedFormSpans -- is what actually characterises the risk
// this escape hatch must protect against: whether s's own trailing bytes,
// wherever they start, could combine with bytes that have not arrived yet
// to complete ANY tracked secret.
//
// This replaces two earlier, narrower attempts within this same task
// (1g2), both found insufficient by rigorous self-review before ever
// shipping:
//   - round 2 kept back cut = len(s)-max(longest-1,0) unconditionally
//     whenever a protected occurrence still crossed it, which can itself
//     land exactly one byte inside a COMPLETE occurrence of the registry's
//     longest tracked form (e.g. a registered "AAAA" beside
//     "AAAAdb-password-42", buffer ending exactly at the end of a complete
//     occurrence of the longer form) -- permanently splitting it.
//   - round 3 (extendPastResolvableOccurrences, isPrefixOfLongerForm,
//     found insufficient by an independent review before it ever shipped)
//     extended cut forward past a whole matched SPAN whenever that span's
//     own full text was not a prefix of some other, longer tracked form --
//     but a DIFFERENT tracked form's forming prefix can start PARTWAY
//     THROUGH that span (not at the span's own start), which the
//     span-level check never examined at all: e.g. tracked forms "AAAA",
//     "AAAA"+"Y"*16 (the registry's own longest, hence resolvable by
//     round 3's own test) and "Y"*8+"ZZZZ" (shorter, but longer than the
//     8-byte run of "Y" hiding inside the longer form's own tail) --
//     round 3 swept the whole "AAAA"+"Y"*16 occurrence, including the
//     trailing 8 "Y"s that were actually the third form's own forming
//     prefix, stranding it.
//
// This byte-suffix formulation sidesteps both failure modes because it
// does not reason about matched SPANS or their boundaries at all -- it
// asks the one question that is actually load-bearing, independently for
// every possible trailing-byte-count k, against every tracked form,
// regardless of whether any complete match happens to start or end
// nearby. It is computed via the standard KMP failure-function technique
// (longestPrefixSuffixOverlap) per tracked form, keeping the largest
// result, in O(len(form)) time per form -- bounded by MaxSplitGuard, and
// by a small, fixed number of tracked forms, so this stays within the
// same roughly-linear-per-call budget FlushPoint already spends on
// protectedFormSpans/protectedCrossing for the very same call.
//
// RESIDUAL, NARROWED (NOT CLOSED) BY resolveForcedFlushCut (task 1g2 round
// 5 / task sg2): this function protects against a not-yet-complete
// occurrence being stranded, but on its own it does NOT guarantee that
// cut = len(s) - this result avoids splitting a SEPARATE, already-complete,
// fully-visible occurrence of a DIFFERENT tracked form G that happens to
// sit embedded within the same dense, gapless, self-overlapping chain that
// forced this call at all (e.g. a periodic driver form C with no gaps
// anywhere in its own coverage, a distinct tracked secret G nested at a
// fixed phase inside C's own repeating unit, and a third, adversarially
// constructed form H whose own proper prefix happens to exactly reproduce
// s's actual trailing bytes for long enough to retreat cut into a PAST
// occurrence of G). Ordinary protectedCrossing, run against every tracked
// form's spans together, cannot resolve this either: when C's own coverage
// genuinely has no gap anywhere, no cut position at all is simultaneously
// safe from splitting G and bounded (retreating far enough to protect every
// possible G strictly requires giving up on making any progress at all for
// as long as the adversarial, perfectly periodic stream continues, i.e.
// exactly the unbounded growth task mb2 and task le2 both fixed) --
// PROVIDED protectedCrossing is asked to protect C too. The insight
// resolveForcedFlushCut acts on is that it does not have to: C is exactly
// the one form that can never be protected here no matter what, so
// excusing it from the check (and it alone, judged form by form, not by
// guessing which form is "the driver") lets protectedCrossing find G's own
// natural gaps -- G's occurrences, considered on their own, are NOT
// themselves gapless (a periodic driver's own repeat period is longer than
// each nested form's length, e.g. G's own 4-byte occurrence leaves 1 byte
// of C's 5-byte unit uncovered every cycle), so a cut can always retreat to
// one of them without giving up boundedness. See resolveForcedFlushCut's
// own doc for the mechanism (confirmed against both task sg2 repro
// constructions, secret/values_test.go) and for the shape it still does not
// close: several nested forms whose occurrences tile the driver's unit
// between them, so no form is exempt yet their union has no gap either.
func (v *Values) longestKeepBackForForcedFlush(s string) int {
	kMax := 0
	for _, e := range v.snapshot() {
		if len(e.form) > MaxSplitGuard {
			continue
		}
		if k := longestPrefixSuffixOverlap(e.form, s); k > kMax {
			kMax = k
		}
	}
	return kMax
}

// resolveForcedFlushCut is the "self-healing driver exemption" fix for task
// sg2 (task 1g2 round 5's documented residual, see
// longestKeepBackForForcedFlush's own doc for the full history and proof
// sketch). It starts from kMaxCut := len(s) -
// longestKeepBackForForcedFlush(s), which already protects every tracked
// form's own not-yet-visible completion, and additionally guards against
// kMaxCut landing inside a SEPARATE, already-complete occurrence of a
// DIFFERENT tracked form:
//
//   - For every tracked form (no longer than MaxSplitGuard, as
//     protectedFormSpans already restricts to), it judges that form ALONE against
//     protectedCrossing, using ONLY that form's own occurrences and
//     kMaxCut as the cut: snap == 0 && crossed == true means literally no
//     point in [0, kMaxCut) clears even one occurrence of this form by
//     itself -- it is a genuinely gapless, self-overlapping driver over the
//     whole region (e.g. a periodic form C whose repeat period divides
//     evenly, chaining every occurrence into the next with no byte ever
//     left uncovered). No cut position can protect such a form without
//     giving up bounded progress entirely (the same trade-off
//     longestKeepBackForForcedFlush's doc already proves), so it is
//     exempted from the check below -- and ONLY such a form: this is
//     decided per form, independently, never by assuming "the longest
//     form" or "the form that produced kMax" is the driver.
//   - Every OTHER tracked form's occurrences are unioned (sorted by start,
//     as protectedCrossing requires) and checked together with the same
//     protectedCrossing frontier search FlushPoint already uses elsewhere:
//     a form not exempted above is, by construction, not gapless on its
//     own, so it has at least one real gap at or below kMaxCut (e.g. a
//     form G nested at a fixed phase inside a driver's repeating unit
//     leaves the rest of that unit uncovered every cycle) for
//     protectedCrossing to snap back to -- exactly what task sg2's second
//     repro construction needed: G's own occurrences are NOT gapless (a
//     1-byte gap opens between consecutive ones), so this search retreats
//     kMaxCut to the nearest such gap and G is never split.
//
// When that union search finds no safe point either (snap == 0: no single
// form is individually gapless, yet their combined coverage has no shared
// gap anywhere below kMaxCut), kMaxCut is used unchanged -- the same answer
// this function replaces, so no regression, but NOT safe: KNOWN, STILL-OPEN
// RESIDUAL (found by task sg2's own adversarial review). It needs no
// engineered form at all, just two nested forms that together tile a
// driver's unit: driver "RMNOP"+"RMNOP" with "MNOP" ([5k+1,5k+5)) and
// "PRMN" ([5k+4,5k+8)) tracked leaves no point in the union that is not
// strictly inside some occurrence, and for a buffer starting at phase 1 of
// the unit, with a trailing proper-prefix form retaining 9 bytes, the cut
// splits an "MNOP" and Redact of the retained tail leaves "NO" raw. Every cut
// there splits some occurrence, so a clean cut does not exist, but a
// leak-free one can: e.g. cutting at the START of a "PRMN" occurrence
// strands only the split "MNOP"'s final "P", which is itself the first byte
// of that complete, fully retained "PRMN" occurrence and so is still
// redacted. A candidate criterion for a follow-up: pick the largest
// occurrence start c <= kMaxCut such that every byte in [c, b) of each
// occurrence [a, b) straddling c is covered by some complete occurrence
// lying entirely in [c, len(s)). It is not implemented here; a naive search
// is quadratic in the window, and bounding it soundly needs its own proof.
// A small snap from this search (e.g. 1, when the union becomes gapless one
// byte in) makes little progress on that call, but the retained buffer then
// starts inside the gapless union, so the next forced call takes this
// fallback and progresses: amortised pending stays bounded (measured at
// flushStallCap+O(chunk) for 1 and 2 MiB of that shape).
//
// byForm is the SAME scan FlushPoint's caller already built
// (protectedFormSpans) for the ordinary, pre-forced-flush protectedCrossing
// check, passed in rather than re-scanned here: task sg2's own self-review
// found that a second, independent per-form scan of s measurably slowed
// this call under `go test -race` (see protectedFormSpans' own doc).
func (v *Values) resolveForcedFlushCut(s string, byForm map[string][][2]int) int {
	kMaxCut := len(s) - v.longestKeepBackForForcedFlush(s)
	var nonExempt [][2]int
	for _, spans := range byForm {
		if snap, crossed := protectedCrossing(spans, kMaxCut); snap == 0 && crossed {
			continue // Exempt: this form alone is a gapless driver here.
		}
		nonExempt = append(nonExempt, spans...)
	}
	if len(nonExempt) == 0 {
		return kMaxCut
	}
	slices.SortFunc(nonExempt, func(a, b [2]int) int { return a[0] - b[0] })
	if snap, _ := protectedCrossing(nonExempt, kMaxCut); snap > 0 {
		return snap
	}
	return kMaxCut
}

// longestPrefixSuffixOverlap returns the length of the longest PROPER
// prefix of form (at most len(form)-1 bytes) that is also a suffix of s,
// using the KMP failure-function technique (no separator byte needed,
// since form and s may be arbitrary bytes, including binary secret
// material): build form's own failure array (kmpFailureFunction), then run
// the KMP search automaton over only the relevant tail of s
// (kmpSuffixMatchLength) -- at most len(form)-1 bytes, since nothing more
// distant could matter (a match longer than that would mean s contains
// form as a genuine substring, a different, already-handled case).
func longestPrefixSuffixOverlap(form, s string) int {
	maxK := len(form) - 1
	if maxK <= 0 {
		return 0
	}
	if maxK > len(s) {
		maxK = len(s)
	}
	if maxK <= 0 {
		return 0
	}
	pattern := form[:maxK]
	tail := s
	if len(tail) > maxK {
		tail = tail[len(tail)-maxK:]
	}
	return kmpSuffixMatchLength(pattern, tail, kmpFailureFunction(pattern))
}

// kmpFailureFunction returns pattern's KMP failure (partial-match) array:
// failure[i] is the length of the longest proper prefix of pattern[:i+1]
// that is also a suffix of pattern[:i+1]. Standard construction.
func kmpFailureFunction(pattern string) []int {
	failure := make([]int, len(pattern))
	k := 0
	for i := 1; i < len(pattern); i++ {
		for k > 0 && pattern[i] != pattern[k] {
			k = failure[k-1]
		}
		if pattern[i] == pattern[k] {
			k++
		}
		failure[i] = k
	}
	return failure
}

// kmpSuffixMatchLength runs the KMP search automaton for pattern (using
// its own failure array) over tail, and returns the match length at the
// very end of the scan: the length of the longest prefix of pattern that
// is also a suffix of tail. A full (length-len(pattern)) match can only
// complete exactly at tail's last character, because
// longestPrefixSuffixOverlap always calls this with len(tail) <=
// len(pattern) (see its own doc): the standard "reset via failure on a
// full match" step (needed so the automaton can keep scanning for a
// possible LATER, overlapping match) therefore only ever discards a full
// match that occurred strictly before the final character, never the one
// this function is actually asked for, so the fullMatchAtEnd bookkeeping
// below is exactly the exception that step must not apply to.
func kmpSuffixMatchLength(pattern, tail string, failure []int) int {
	match := 0
	fullMatchAtEnd := false
	for i := 0; i < len(tail); i++ {
		for match > 0 && tail[i] != pattern[match] {
			match = failure[match-1]
		}
		if tail[i] == pattern[match] {
			match++
		}
		if match == len(pattern) {
			if i == len(tail)-1 {
				fullMatchAtEnd = true
			}
			match = failure[match-1]
		}
	}
	if fullMatchAtEnd {
		return len(pattern)
	}
	return match
}

// MaxPending implements logger.Redactor: RedactingWriter must not force a
// flush before an unterminated line reaches MaxSplitGuard bytes, because
// below that threshold FlushPoint cannot even keep back its longest
// tracked form (len(s) - (longest-1) <= 0) and would return "", 0 on every
// call, growing pending without bound (the bug task mb2 fixed). Reaching
// MaxSplitGuard does not by itself guarantee progress on every later call,
// though: FlushPoint's escape hatch (see above) can still stall past it for
// a densely self-overlapping match chain that keeps finding no safe cut —
// what bounds THAT stall is flushStallCap, not MaxPending (task le2: task
// rd2's leak fix correctly made the escape hatch refuse an unsafe cut here,
// which reintroduced mb2's unbounded growth for this one input shape until
// flushStallCap capped it).
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
		spans = append(spans, formOccurrences(e.form, s)...)
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
