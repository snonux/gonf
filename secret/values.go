package secret

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
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
// never produce, and instead forces progress: it recomputes the keep-back
// directly from s's own trailing bytes (longestKeepBackForForcedFlush, see
// FlushPoint's own doc) instead of trusting the naive len(s)-(longest-1)
// figure, then retreats that cut to a leak-free one
// (resolveForcedFlushCut). A capped flush retains fewer than
// 2*MaxSplitGuard bytes (resolveForcedFlushCut proves the bound), so
// pending peaks near the cap plus one write -- not the unbounded growth
// task mb2 fixed.
//
// This restores the bounded-pending, bounded-rescan invariant task mb2
// established (bounded, not linear: see FlushPoint's "Cost" paragraph for
// what small writes against a dense self-overlapping form still cost
// within it): without a cap, a caller such as logger.RedactingWriter
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
	// set only grows, except for Reset, and a form's mode only gains
	// matching (merge): the forced flush's leak-freedom induction relies on
	// that (see resolveForcedFlushCut).
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
	//
	// The raw form (e.g. "hunter22!\n") is only needed for whole-payload
	// matching of a short secret: as a substring pattern it would redact
	// the line break after the secret too, joining two output lines, and
	// the TrimRight form already covers every such occurrence.
	forms := []string{trimmed, strings.TrimRight(raw, "\r\n")}
	if !mode.contained {
		forms = append(forms, raw)
	}
	for _, form := range forms {
		v.addForm(form, mode)
	}
	for _, line := range strongLines(trimmed) {
		v.addForm(line, formMode{contained: true, strong: true, redactOnly: true})
	}
	v.sorted = nil
}

// addForm tracks form, its JSON escaping and its Go %q escaping (errors and
// logs quote line content with %q, which escapes differently from JSON)
// with mode, merged with the mode of any secret already sharing it. The
// caller holds v.mu.
func (v *Values) addForm(form string, mode formMode) {
	if form == "" {
		return
	}
	for _, f := range []string{form, jsonEscaped(form), goQuoted(form)} {
		if old, ok := v.forms[f]; ok {
			v.forms[f] = old.merge(mode)
		} else {
			v.forms[f] = mode
		}
	}
}

// goQuoted returns form as strconv.Quote (and so %q) writes it, without
// the surrounding quotes.
func goQuoted(form string) string {
	q := strconv.Quote(form)
	return q[1 : len(q)-1]
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
// split between two redactions. Matches are sorted by start and merged in
// one linear pass (mergeSpans) rather than chased backward one
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
// grow the buffer without bound. There the cut still splits some
// occurrence (none can be avoided), so resolveForcedFlushCut retreats it to
// a leak-free one: every byte of the retained tail that belongs to a split
// occurrence is also covered by a complete occurrence inside that tail, so
// a later Redact still hides it (task tg2; see that function for the
// bounded-search proof).
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
// their account would defeat the point of excluding them.
//
// Cost: one call scans s once per contained form (matchSpans), scans the
// flushed prefix again (Redact) or, on the escape hatch, s again
// (protectedSpans), and sorts the occurrences found (mergeSpans). Each scan
// is roughly O(len(s)+len(form)) per form (formOccurrences switches to KMP
// once occurrences overlap), but a densely self-overlapping form still
// yields up to one occurrence per byte of s, so the sort makes a dense call
// O(n log n) in its n occurrences. Before task 0h2, formOccurrences
// restarted strings.Index one byte past every occurrence, O(len(s)*L) for
// a dense form of length L: relaying 1 MiB of '=' in 32 KiB writes with a
// 32 KiB '=' secret took about 6 s (7 s under -race, 13.6 s in task tg2's
// review) and takes about 0.2 s (1.3 s under -race) since. What task 0h2
// did not change: the relay calls FlushPoint on every Write while its
// unterminated line exceeds MaxSplitGuard, and while the escape hatch
// stalls (from there up to flushStallCap) each of those calls rescans the
// whole pending buffer, so the cost per relayed byte grows as writes
// shrink: a 40-byte '=' secret over 1 MiB of '=' takes about 0.3 s at
// 32 KiB writes, 2 s at 4 KiB and 15 s at 512 B (0.75 s, 4.8 s and 27 s in
// that review). That is bounded, since pending never passes flushStallCap
// plus one write, but not linear in the write count. It implements
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
			return v.escapeHatchFlush(s, cut)
		}
		cut = run[0]
		break
	}
	if cut <= 0 {
		return "", 0
	}
	return v.Redact(s[:cut]), cut
}

// escapeHatchFlush is FlushPoint's escape hatch, for a buffer longer than
// MaxSplitGuard whose crossing merged run starts at 0 (see FlushPoint's doc):
// cut is the ordinary keep-back cut, which that run crosses. It first looks
// for a gap-free point at or before cut (protectedCrossing) and takes it
// when one exists above 0. Without one it stalls (returns no progress) while
// s is still short of flushStallCap, and past the cap it forces progress
// through resolveForcedFlushCut. It must NOT simply consume the whole buffer
// (task 1g2 round 1: cut = len(s) strands an in-progress longer occurrence's
// swept prefix), keep the naive cut (round 2: crossed == true means some
// occurrence straddles it by definition), or extend past whole matched
// spans judged by their own text (round 3: a different form's forming
// prefix can start partway through such a span). The flushed prefix is
// always the single opaque Redacted marker: it is run material with nothing
// legitimate ahead of it (see FlushPoint's doc).
func (v *Values) escapeHatchFlush(s string, cut int) (out string, consumed int) {
	spans := v.protectedSpans(s)
	snap, crossed := protectedCrossing(spans, cut)
	switch {
	case snap > 0:
		cut = snap
	case crossed && len(s) < flushStallCap:
		// Still short of the cap: keep waiting for a gap rather than
		// guess (see flushStallCap's doc).
		return "", 0
	case crossed:
		cut = v.resolveForcedFlushCut(s, spans)
	}
	if cut <= 0 {
		// Unreachable past flushStallCap (resolveForcedFlushCut's doc
		// proves cut > len(s)-2*MaxSplitGuard there); kept so a future
		// change to the cap can only stall, never emit an empty flush.
		return "", 0
	}
	return Redacted, cut
}

// protectedSpans returns the occurrences of every tracked, contained form no
// longer than MaxSplitGuard in s, sorted by start: the ones FlushPoint's
// keep-back is actually sized to protect (see FlushPoint's doc). It is a
// deliberately separate, independent scan from matchSpans rather than a
// filter applied to its result: matchSpans' slice is sorted in place as a
// side effect of the mergeSpans call FlushPoint already made on it, and a
// second, differently-filtered view built by reordering that same backing
// array would risk a subtle span/metadata desync. The one sorted slice
// serves both protectedCrossing and resolveForcedFlushCut, so s is scanned
// once per escape-hatch call (task sg2 found a second per-form rescan
// measurably slower under -race, close to the historical-shapes test's
// deadline). Each form's occurrences already come out sorted, so they are
// merged pairwise (mergeByStart, O(n log forms)) instead of re-sorted: task
// tg2's profile showed the general sort as the largest single cost of a
// dense escape-hatch call.
func (v *Values) protectedSpans(s string) [][2]int {
	var lists [][][2]int
	for _, e := range v.snapshot() {
		if e.contained && len(e.form) <= MaxSplitGuard {
			if occ := formOccurrences(e.form, s); len(occ) > 0 {
				lists = append(lists, occ)
			}
		}
	}
	for len(lists) > 1 {
		merged := lists[:0:0]
		for i := 0; i < len(lists); i += 2 {
			if i+1 == len(lists) {
				merged = append(merged, lists[i])
			} else {
				merged = append(merged, mergeByStart(lists[i], lists[i+1]))
			}
		}
		lists = merged
	}
	if len(lists) == 0 {
		return nil
	}
	return lists[0]
}

// mergeByStart merges two span lists that are each sorted by start into one
// list sorted by start.
func mergeByStart(a, b [][2]int) [][2]int {
	out := make([][2]int, 0, len(a)+len(b))
	for len(a) > 0 && len(b) > 0 {
		if b[0][0] < a[0][0] {
			out, b = append(out, b[0]), b[1:]
		} else {
			out, a = append(out, a[0]), a[1:]
		}
	}
	return append(append(out, a...), b...)
}

// formOccurrences returns the byte ranges of every occurrence (overlaps
// included) of form alone in s, in start order. It is the one place that
// walks a single form's occurrences, shared by protectedSpans and matchSpans
// (each unions it over every tracked form).
//
// It scans with strings.Index, restarting one byte past each occurrence,
// for as long as the occurrences found are disjoint: each Index call then
// re-reads at most one occurrence's bytes, so the scan stays roughly
// O(len(s)) and keeps Index's speed for the usual sparse matches. Restarting
// Index would instead re-verify every overlapping occurrence from scratch,
// O(len(s)*len(form)) for a dense run such as a 32 KiB '=' secret over a
// run of '=' (task 0h2), so the first occurrence that overlaps the one
// before it switches the rest of the scan to KMP, which finds every
// remaining occurrence in one pass, O(len(s)+len(form)) (kmpOccurrences).
// The result is exactly the former all-Index loop's
// (TestFormOccurrencesMatchesIndexOracleRandomized): up to the switch the
// loop is unchanged, and from the overlapping occurrence's start on KMP
// reports every occurrence starting there or later, in order. An
// overlapping occurrence needs a form of at least two bytes (it starts
// after the previous one and before its end), so the empty form never
// switches.
func formOccurrences(form, s string) [][2]int {
	var spans [][2]int
	for from := 0; from < len(s); {
		i := strings.Index(s[from:], form)
		if i < 0 {
			break
		}
		start := from + i
		if len(spans) > 0 && start < spans[len(spans)-1][1] {
			return append(spans, kmpOccurrences(form, s, start, kmpFailureFunction(form))...)
		}
		spans = append(spans, [2]int{start, start + len(form)})
		from = start + 1
	}
	return spans
}

// kmpOccurrences returns every occurrence, overlaps included, of a
// non-empty form in s that starts at or after first, in start order, with
// the KMP search automaton (failure is form's kmpFailureFunction array).
// After a full match it falls back to failure's last entry rather than to
// 0, so a later occurrence overlapping this one is still found.
func kmpOccurrences(form, s string, first int, failure []int) [][2]int {
	var spans [][2]int
	match := 0
	for i := first; i < len(s); i++ {
		for match > 0 && s[i] != form[match] {
			match = failure[match-1]
		}
		if s[i] == form[match] {
			match++
		}
		if match == len(form) {
			spans = append(spans, [2]int{i + 1 - len(form), i + 1})
			match = failure[match-1]
		}
	}
	return spans
}

// protectedCrossing scans spans (sorted by start, as protectedSpans provides
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
// call, since nothing was ever forwarded to shrink it — a full scan of s
// (see FlushPoint's "Cost" paragraph), repeated on every still-unresolved
// call, so cost would grow roughly with the square of how long a genuinely
// never-breaking chain persists if the stall were allowed to continue
// unbounded. It is not: escapeHatchFlush still calls protectedCrossing
// first on every call, but once s reaches flushStallCap a crossed result no
// longer stalls. It forces progress instead: it keeps back
// longestKeepBackForForcedFlush's kMax trailing bytes, retreats that cut to
// a leak-free one (resolveForcedFlushCut, which retains fewer than
// 2*MaxSplitGuard bytes), and redacts the flushed prefix [0, cut) as one
// opaque Redacted marker rather than consuming everything (see
// flushStallCap). The stall's rescanning is therefore capped per cycle:
// with writes of w bytes, about flushStallCap/w stalled calls each scan up
// to flushStallCap bytes, before pending drops back below 2*MaxSplitGuard
// and the cycle starts over — a bounded, repeating cost rather than one
// that keeps compounding for as long as a pathological stream continues
// (task le2; an earlier version of this comment called the unbounded
// growth "accepted", which task rd2's leak fix had reintroduced as a live
// regression rather than a deliberate trade-off). Bounded is not cheap,
// though: small writes pay that rescan many times per cycle (FlushPoint's
// "Cost" paragraph has the figures). It can only arise when literally no
// gap exists anywhere in the buffer given so far (a perfectly, densely
// self-overlapping run), which the fix above already finds and exploits
// every gap to avoid whenever one exists.
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
// by matchSpans/protectedSpans -- is what actually characterises the risk
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
// result, in O(len(form)) time per form -- O(sum of form lengths) per
// call, each form bounded by MaxSplitGuard and independent of len(s), so
// it is small next to the scan of s that protectedSpans already did for
// the very same call (see FlushPoint's "Cost" paragraph).
//
// This function only keeps a not-yet-complete occurrence from being
// stranded; on its own, cut = len(s) - this result can still split a
// SEPARATE, already-complete occurrence inside the dense chain that forced
// the call (task 1g2 round 5 / task sg2: a periodic driver with a form
// nested at a fixed phase of its unit and an engineered proper-prefix form
// retreating the cut into a past nested occurrence; task tg2: two nested
// forms tiling the driver's unit so no gap-free cut exists at all).
// resolveForcedFlushCut starts from that cut and retreats it to a leak-free
// one; see its doc for the criterion and the bounded-search proof.
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

// resolveForcedFlushCut returns the cut FlushPoint's escape hatch uses once
// s has reached flushStallCap with no gap-free point anywhere (the protected
// occurrences in spans, sorted by start, tile the whole region). It starts
// from kMaxCut := len(s) - longestKeepBackForForcedFlush(s), which keeps
// every not-yet-complete occurrence's visible start in the retained tail,
// and retreats from there to the largest LEAK-FREE cut its search reaches.
//
// No gap-free cut exists here, so the cut must split some occurrence; what
// matters is only whether a raw byte of it can escape. The flushed prefix
// [0, c) becomes one opaque Redacted marker, so only the retained tail
// s[c:] can leak: a byte of it that belongs to an occurrence starting
// before c (a straddler) but to no complete occurrence inside s[c:] would
// be forwarded raw, because Redact never matches a partial occurrence (a
// later occurrence might happen to cover it, but nothing guarantees one).
// A byte that IS covered by a complete occurrence inside s[c:] is safe. The
// retained tail (which holds no newline: the relay only forces a flush of
// an unterminated line) later leaves the relay by one of three paths, and
// each keeps that covering occurrence whole:
//   - Close redacts whatever is pending in one Redact call;
//   - a newline arriving later makes the relay forward pending up to and
//     including it as one line through Redact (logger.RedactingWriter's
//     Write); the whole tail lies inside that line, since the newline comes
//     after it;
//   - FlushPoint runs again on the grown buffer: its ordinary path only cuts
//     between merged runs, and its escape hatch always emits the flushed
//     prefix as one opaque Redacted marker and applies this same rule to
//     the new buffer.
//
// So by induction every byte of a split occurrence ends up redacted. The
// induction assumes the covering occurrence is still matched later, which
// holds because a Values' forms only ever grow: Add inserts forms and
// merges modes (formMode.merge only turns contained on, never off), and
// never removes one. Reset breaks it: forgetting the forms between two
// writes of one relayed line (tests only) can forward a retained tail's
// split bytes raw. A cut c is
// therefore leak-free when the contiguous coverage of [c, ...) by complete
// occurrences starting at or after c (coverFrom) reaches the farthest end of
// any straddler (straddlerReach). This admits the cuts task sg2 documented
// as missing (task tg2): driver "RMNOP"+"RMNOP" with nested "MNOP" and
// "PRMN" tile the unit so that every cut splits something, but a cut at the
// start of a driver occurrence is leak-free (that occurrence covers every
// straddler's tail). It also retires sg2's per-form "gapless driver exemption", which
// existed only because the old gap-free test could never protect a driver;
// this test protects drivers like every other form.
//
// The search: while c is not leak-free, move c to the start of the
// straddler with the farthest end, need(c). Why that is bounded and always
// makes progress:
//   - Each step moves to an occurrence O' that strictly contains the
//     previous one, O: O' starts before c = start(O), and need(c) >
//     coverFrom(c) >= end(O), because O itself starts at c. The first O
//     already contains kMaxCut, so every O visited does too. Lengths strictly
//     increase, so the search takes at most one step per distinct protected
//     form length; each step inspects only occurrences starting within the
//     longest protected form of c, found by binary search.
//   - It always stops at a leak-free cut: if the visited O is at least as
//     long as every straddler of its own start, each straddler [a, b) with a
//     < c ends before a+len(O) <= c+len(O) = end(O), covered by O itself.
//     Otherwise a longer straddler exists and the search takes one more
//     step; the lengths cannot grow past the longest protected form.
//   - The final c is kMaxCut itself or the start of an occurrence
//     containing kMaxCut, so c > kMaxCut - longest, where longest is the
//     longest protected occurrence in s. longestKeepBackForForcedFlush
//     returns less than the longest form no longer than MaxSplitGuard, so
//     the retained tail is shorter than kMax + longest < 2*MaxSplitGuard,
//     and len(s) >= flushStallCap = 4*MaxSplitGuard makes c >
//     2*MaxSplitGuard > 0. Every forced call therefore drains all but that
//     bounded tail (invariant B).
//   - Cost: each step is two binary searches plus O(forms * longest) span
//     visits (straddlerReach inspects starts in (c-longest, c), coverFrom
//     starts in [c, need) with need <= c+longest, and each protected form
//     has at most one occurrence per start), and there are at most forms
//     steps (one per distinct length), so the retreat costs
//     O(forms^2 * longest) per forced call, longest <= MaxSplitGuard. That
//     comes on top of longestKeepBackForForcedFlush's O(sum of form
//     lengths) and the scan of s protectedSpans already did (see
//     FlushPoint's "Cost" paragraph). And c strictly decreases, so the loop
//     terminates even if this argument had a flaw.
func (v *Values) resolveForcedFlushCut(s string, spans [][2]int) int {
	longest := 0
	for _, sp := range spans {
		longest = max(longest, sp[1]-sp[0])
	}
	c := len(s) - v.longestKeepBackForForcedFlush(s)
	for {
		need, from := straddlerReach(spans, c, longest)
		if need <= c || coverFrom(spans, c, need) >= need {
			return c
		}
		c = from
	}
}

// straddlerReach returns the farthest end (need) of any occurrence in spans
// (sorted by start) that straddles c, i.e. [a, b) with a < c < b, and the
// start (from) of the latest-starting straddler reaching that end; need is
// c when nothing straddles c. A straddler is at most longest bytes long, so
// only occurrences starting in (c-longest, c) are inspected.
func straddlerReach(spans [][2]int, c, longest int) (need, from int) {
	need, from = c, c
	i, _ := slices.BinarySearchFunc(spans, c, func(sp [2]int, t int) int { return sp[0] - t })
	for i--; i >= 0 && spans[i][0] > c-longest; i-- {
		if spans[i][1] > need {
			need, from = spans[i][1], spans[i][0]
		}
	}
	return need, from
}

// coverFrom returns how far the occurrences in spans (sorted by start) that
// start at or after c cover [c, ...) without a hole, stopping early once
// that reaches need: c itself when no occurrence starts at c. Occurrences
// that merely touch ([x, y) then [y, z)) are contiguous, since the byte y
// belongs to the second.
func coverFrom(spans [][2]int, c, need int) int {
	frontier := c
	i, _ := slices.BinarySearchFunc(spans, c, func(sp [2]int, t int) int { return sp[0] - t })
	for ; i < len(spans) && spans[i][0] <= frontier && frontier < need; i++ {
		frontier = max(frontier, spans[i][1])
	}
	return frontier
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
