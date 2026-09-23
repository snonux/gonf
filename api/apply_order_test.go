package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/plan"
)

// perfRuns is how many interleaved (small, large) sample pairs
// growthRatioSamples collects for one measurement of assertGrowsSubQuadratically.
// It no longer needs to be odd -- that constraint came from the retired
// median-and-majority early exit, and the minimum estimator that replaced
// it (see growthRatioSamples) has no such requirement.
const perfRuns = 5

// perfRatioBound is the most a 4x-size run's minimum time may be over the
// base-size run's minimum time: a linear or O(n log n) algorithm grows
// about 4x under a 4x input, an O(n^2) algorithm about 16x. The bound was
// raised from an earlier 8x to 12x -- closer to the quadratic tell (16x)
// than to linear (4x) -- to give the minimum estimator and interleaved
// sampling (see growthRatioSamples) more headroom against a load burst
// that still manages to land more on the large size's samples than the
// small size's despite interleaving, without weakening the check's
// ability to catch a quadratic or worse regression.
const perfRatioBound = 12.0

// perfHangGuard is an absolute ceiling on ONE timed sample (see
// timedSample), independent of load. It exists only to fail a genuine hang
// (an infinite loop, or an old quadratic/cubic implementation run against a
// size large enough to take minutes) rather than bound normal variance, so
// it is generous. The race detector roughly tenfolds the algorithm's own
// cost on top of whatever the machine is already doing, so it gets extra
// room.
func perfHangGuard() time.Duration {
	if raceEnabled {
		return 120 * time.Second
	}
	return 60 * time.Second
}

// timedSample runs fn once, in its own goroutine, bounded by perfHangGuard,
// and returns its wall-clock duration. If fn does not return within the
// guard, or returns a non-nil error (a correctness check inside fn
// failed), the test fails immediately naming label -- always on the test's
// own goroutine, via t.Fatalf here, never inside fn's goroutine, since
// FailNow must run on the goroutine executing the test. Without a
// per-sample bound, a single catastrophically slow sample (a reintroduced
// quadratic/cubic orderForPrivilegeSplit can take minutes even at the
// smaller of the two sizes these tests compare) would run to completion --
// or past Go's own test-binary timeout, aborting the whole run with an
// unlabelled panic dump instead of this test's named failure -- before
// growthRatioSamples ever got to look at it. This is the one early exit
// assertGrowsSubQuadratically relies on: a hang ends the test immediately,
// without waiting for the rest of the samples at either size. On timeout,
// fn's goroutine is abandoned (Go cannot cancel a running goroutine); that
// is safe here because fn only reads its captured plan, and it is
// acceptable because the test has already failed.
func timedSample(t *testing.T, label string, fn func() error) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	guard := perfHangGuard()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return time.Since(start)
	case <-time.After(guard):
		t.Fatalf("%s did not return within the %v hang guard (a hang, or a quadratic/cubic regression)", label, guard)
		return 0 // unreached: t.Fatalf ends this goroutine
	}
}

// growthRatioSamples runs small and large interleaved -- one small sample,
// then one large sample, repeated perfRuns times -- rather than all of
// small's samples followed by all of large's. A load burst confined to
// part of the run (a neighbouring worktree's -race suite starting up, a GC
// sweep, the kernel scheduling this goroutine off a busy core) then falls
// across both sizes instead of landing entirely inside one size's batch
// and skewing its samples relative to the other's.
//
// It returns the minimum of each size's samples rather than their median:
// noise can only ADD time to a sample, never remove it, so the minimum
// across repeated samples is the least noise-sensitive estimate of the
// operation's intrinsic cost at that size -- one sample that happens to
// run unburdened is enough to pull the minimum down to the true cost,
// whereas every single sample being inflated (the whole run sitting inside
// one sustained load burst) is what it would take to inflate the minimum
// too. Unlike the retired median-and-majority approach, no early exit is
// mathematically sound here: an additional sample can only lower a size's
// minimum, so a large sample currently above threshold might still be
// rescued by a later, faster one, and a small sample currently keeping the
// threshold loose might still tighten it later -- so all perfRuns samples
// of both sizes are always taken (bounded, per sample, by timedSample's
// hang guard).
func growthRatioSamples(t *testing.T, smallLabel string, small func() error, largeLabel string, large func() error) (smallMin, largeMin time.Duration) {
	t.Helper()
	for i := range perfRuns {
		if d := timedSample(t, smallLabel, small); i == 0 || d < smallMin {
			smallMin = d
		}
		if d := timedSample(t, largeLabel, large); i == 0 || d < largeMin {
			largeMin = d
		}
	}
	return smallMin, largeMin
}

// assertGrowsSubQuadratically measures small and large (the same operation
// as small, at 4x its input size) and fails if large's minimum time grew
// more than perfRatioBound times small's minimum time (see
// growthRatioSamples for how the two sizes are sampled and estimated).
// Growth, not an absolute wall-clock bound, is what is asserted, so the
// check stays valid on a heavily loaded machine (both runs slow down
// together) while still catching a quadratic or worse regression, such as
// the historic fixpoint solver and the per-watch deletion-trial search the
// two callers guard against.
//
// A single measurement that comes in over the bound is retried once, with
// an entirely fresh set of interleaved samples, before the test is failed:
// flakiness from a load burst that still manages to skew one size's
// samples despite interleaving (see growthRatioSamples) then needs two
// independent bad draws in a row, not one, to fail the test. The retry
// only masks noise, not a real regression: a reintroduced quadratic/cubic
// algorithm is slow on every sample at the larger size, so it fails the
// same way on the retry too (or hits timedSample's hang guard first).
func assertGrowsSubQuadratically(t *testing.T, smallLabel string, small func() error, largeLabel string, large func() error) {
	t.Helper()
	const attempts = 2
	for attempt := 1; attempt <= attempts; attempt++ {
		smallMin, largeMin := growthRatioSamples(t, smallLabel, small, largeLabel, large)
		threshold := time.Duration(float64(max(smallMin, time.Microsecond)) * perfRatioBound)
		if largeMin <= threshold {
			return
		}
		if attempt < attempts {
			ratio := float64(largeMin) / float64(max(smallMin, time.Microsecond))
			t.Logf("time grew %.1fx from %s (%v) to %s (%v); want < %.1fx; retrying once before failing",
				ratio, smallLabel, smallMin, largeLabel, largeMin, perfRatioBound)
			continue
		}
		ratio := float64(largeMin) / float64(max(smallMin, time.Microsecond))
		t.Fatalf("time grew %.1fx from %s (%v) to %s (%v); want < %.1fx (quadratic growth would be about 16x); failed again on retry",
			ratio, smallLabel, smallMin, largeLabel, largeMin, perfRatioBound)
	}
}

var orderHdr = plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}

// orderOp builds a command op for the ordering tests.
func orderOp(id string, elevate bool, deps ...string) plan.Op {
	return plan.Op{Op: plan.KindCommand, ID: id, Elevate: elevate, Deps: deps, Payload: plan.CommandPayload{Bin: "true"}}
}

// bodyIDs lists the op IDs after the plan header.
func bodyIDs(t *testing.T, ops []plan.Op) string {
	t.Helper()
	if ops[0].Op != plan.KindPlan {
		t.Fatalf("header moved: %+v", ops[0])
	}
	ids := make([]string, 0, len(ops)-1)
	for _, o := range ops[1:] {
		ids = append(ids, o.ID)
	}
	return strings.Join(ids, ",")
}

// TestOrderForPrivilegeSplit pins the ordering Apply splits: deps first,
// privilege classes kept together, the fewer-chunk order of the two starting
// classes, and ties in incoming order.
func TestOrderForPrivilegeSplit(t *testing.T) {
	op := orderOp
	cases := []struct {
		name string
		ops  []plan.Op
		want string
	}{
		{"dependency before dependent", []plan.Op{orderHdr, op("a", false, "c"), op("b", true, "c"), op("c", false)}, "c,a,b"},
		{"classes grouped", []plan.Op{orderHdr, op("a", true), op("b", false), op("c", true), op("d", false)}, "a,c,b,d"},
		// Starting with a's class gives a|b|c (3 chunks); starting with the
		// elevated class gives b|a,c (2 chunks), which must win.
		{"fewest chunks over both starting classes", []plan.Op{orderHdr, op("a", false), op("b", true), op("c", false, "b")}, "b,a,c"},
		{"equal chunk counts keep the lowest-index start", []plan.Op{orderHdr, op("a", false), op("b", true)}, "a,b"},
		{"dangling dep ignored", []plan.Op{orderHdr, op("a", true, "typo"), op("b", false)}, "a,b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := orderForPrivilegeSplit(tc.ops)
			if err != nil {
				t.Fatalf("orderForPrivilegeSplit() error = %v", err)
			}
			if ids := bodyIDs(t, got); ids != tc.want {
				t.Fatalf("order = %s, want %s", ids, tc.want)
			}
		})
	}
}

// TestOrderForPrivilegeSplitRefusesCycles pins that every dependency cycle is
// refused with exactly the loop named: a self-dependency (the smallest
// cycle) and a lower-indexed op that depends on a cycle without being on it
// (z -> x with x <-> y), which must not appear in the named loop.
func TestOrderForPrivilegeSplitRefusesCycles(t *testing.T) {
	op := orderOp
	cases := []struct {
		name string
		ops  []plan.Op
		want string
	}{
		{"two-op cycle", []plan.Op{orderHdr, op("a", true, "b"), op("b", false, "a")}, "a -> b -> a"},
		{"cycle behind an acyclic prefix", []plan.Op{orderHdr, op("e", true), op("x", false, "y"), op("y", false, "x"), op("z", false, "x")}, "x -> y -> x"},
		{"lead-in op not on the cycle", []plan.Op{orderHdr, op("e", true), op("z", false, "x"), op("x", false, "y"), op("y", false, "x")}, "x -> y -> x"},
		{"self-dependency after elevated op", []plan.Op{orderHdr, op("a", true), op("b", false, "b")}, "b -> b"},
		{"self-dependent elevated op", []plan.Op{orderHdr, op("a", false), op("b", true, "b")}, "b -> b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := orderForPrivilegeSplit(tc.ops)
			want := "Apply: circular dependency: " + tc.want + " (each depends on the next)"
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("orderForPrivilegeSplit() error = %v, want prefix %q", err, want)
			}
		})
	}
}

// TestChunkLevelsStayWithSwitchedClass pins that after a forced class
// switch an op of the class switched TO joins that chunk: with a(F), b(T),
// c(F, needs b), d(T) and level 0 unprivileged, the order is a | b d | c. A
// sort that kept preferring the starting class after the switch would emit
// a | b | c | d (four chunks). (orderForPrivilegeSplit itself picks the
// elevated start here, b d | a c, which needs only two.)
func TestChunkLevelsStayWithSwitchedClass(t *testing.T) {
	body := []plan.Op{orderOp("a", false), orderOp("b", true), orderOp("c", false, "b"), orderOp("d", true)}
	g := newDepGraph(body)
	var ids []string
	for _, i := range g.levelOrder(g.chunkLevels(body, false, nil)) {
		ids = append(ids, body[i].ID)
	}
	if got := strings.Join(ids, ","); got != "a,b,d,c" {
		t.Fatalf("order = %s, want a,b,d,c", got)
	}
	full, _, err := orderForPrivilegeSplit(append([]plan.Op{orderHdr}, body...))
	if err != nil {
		t.Fatal(err)
	}
	if got := bodyIDs(t, full); got != "b,d,a,c" {
		t.Fatalf("orderForPrivilegeSplit = %s, want b,d,a,c", got)
	}
}

// watchOp is orderOp with a change gate on watch.
func watchOp(id string, elevate bool, watch []string, deps ...string) plan.Op {
	op := orderOp(id, elevate, deps...)
	op.IfChanged, op.Watch = true, watch
	return op
}

// TestOrderForPrivilegeSplitKeepsWatchesInOneChunk pins that a change-gated
// op and the same-class ops it watches land in one chunk. The first case is
// the review repro: x(F); e(T, needs x); f(F); g(F, needs e and f, watches
// f). A greedy class sort emits f,x | e | g and splits g from f; the valid
// order x | e | f,g has the same three chunks. The other cases pin the
// chunk count stays minimal and that a watch no order can satisfy (across
// classes, or forced apart by an other-class dependency) leaves the order
// alone for the pre-flight to refuse.
func TestOrderForPrivilegeSplitKeepsWatchesInOneChunk(t *testing.T) {
	op, w := orderOp, watchOp
	cases := []struct {
		name string
		ops  []plan.Op
		want string
	}{
		{"review repro", []plan.Op{orderHdr, op("e", true, "x"), w("g", false, []string{"f"}, "e", "f"), op("f", false), op("x", false)}, "x,e,f,g"},
		{"watch without a dep", []plan.Op{orderHdr, op("e", true, "x"), w("g", false, []string{"f"}, "e"), op("f", false), op("x", false)}, "x,e,g,f"},
		{"no extra chunk when the watch already fits", []plan.Op{orderHdr, op("a", false), w("b", false, []string{"a"}, "a"), op("c", true, "b")}, "a,b,c"},
		{"cross-class watch keeps the dependency order", []plan.Op{orderHdr, op("e", true), w("g", false, []string{"e"}, "e")}, "e,g"},
		// The satisfiable watch (g on x) comes first and the unsatisfiable
		// one (h on f, forced apart by e) second: g must still share x's
		// chunk, only h's watch is dropped.
		{"only the unsatisfiable watch is dropped", []plan.Op{orderHdr, op("e", true, "f"), op("f", false),
			w("g", false, []string{"x"}), w("h", false, []string{"f"}, "e"), op("k", true), op("x", false, "k")}, "f,e,k,g,h,x"},
		{"watch forced apart keeps the dependency order", []plan.Op{orderHdr, op("e", true, "f"), op("f", false), w("g", false, []string{"f"}, "e", "f")}, "f,e,g"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := orderForPrivilegeSplit(tc.ops)
			if err != nil {
				t.Fatalf("orderForPrivilegeSplit() error = %v", err)
			}
			if ids := bodyIDs(t, got); ids != tc.want {
				t.Fatalf("order = %s, want %s", ids, tc.want)
			}
		})
	}
}

// refusedWatchPlan builds a plan of about n ops with w groups of watches
// that must be refused, padded with a dependency chain alternating privilege
// classes. Even groups cannot keep their watch at all (the gated op needs an
// elevated op that needs what it watches); odd groups are the round-6 review
// shape, whose watch only conflicts together with kept ones and so also
// exercises the minimal conflict search.
func refusedWatchPlan(n, w int) []plan.Op {
	ops := []plan.Op{orderHdr}
	for k := range w {
		id := func(name string) string { return fmt.Sprint(name, k) }
		if k%2 == 0 {
			ops = append(ops, orderOp(id("b"), false), orderOp(id("e"), true, id("b")),
				watchOp(id("a"), false, []string{id("b")}, id("e"), id("b")))
			continue
		}
		ops = append(ops, orderOp(id("b"), false), watchOp(id("c"), false, []string{id("b")}),
			orderOp(id("e"), true, id("c")), watchOp(id("a"), false, []string{id("b")}, id("e")),
			watchOp(id("d"), false, []string{id("a")}))
	}
	for k := 0; len(ops) <= n; k++ {
		var deps []string
		if k > 0 {
			deps = []string{fmt.Sprint("p", k-1)}
		}
		ops = append(ops, orderOp(fmt.Sprint("p", k), k%3 == 0, deps...))
	}
	return ops
}

// TestOrderForPrivilegeSplitLargeRefusedPlanIsFast guards against a
// quadratic or worse regression in ordering large refused plans (2000 ops
// with 300 refused watch groups took 5.6s with the old fixpoint solver; the
// greedy's per-watch check searches only what the watch's ends reach, so it
// does not). An absolute wall-clock bound is flaky under machine load (it
// failed twice at a load average of ~24), so this instead measures the same
// plan shape at a base size and at 4x that size and asserts the time grows
// well below quadratic (see assertGrowsSubQuadratically). Both the
// per-sample hang guard and the majority-based early exit it uses mean a
// reintroduced regression fails within about one hang guard of its first
// slow sample, not after running every sample at both sizes to completion.
// Two base sizes, a moderate one and one four times larger, are checked to
// cover both regimes the old bounds did.
func TestOrderForPrivilegeSplitLargeRefusedPlanIsFast(t *testing.T) {
	for _, base := range []struct{ ops, watches int }{{2000, 300}, {2500, 375}} {
		check := func(ops, watches int) func() error {
			built := refusedWatchPlan(ops, watches)
			want := watches / 2
			return func() error {
				_, conflicts, err := orderForPrivilegeSplit(built)
				if err != nil || len(conflicts) != want {
					return fmt.Errorf("orderForPrivilegeSplit() = %v, %d together-conflicts; want no error and %d",
						err, len(conflicts), want)
				}
				return nil
			}
		}
		assertGrowsSubQuadratically(t,
			fmt.Sprintf("refusedWatchPlan(%d ops, %d watches)", base.ops, base.watches), check(base.ops, base.watches),
			fmt.Sprintf("refusedWatchPlan(%d ops, %d watches)", base.ops*4, base.watches*4), check(base.ops*4, base.watches*4))
	}
}

// watchChainPlan is the round-7 review's adversarial shape: a chain of n
// unprivileged ops, u(i) watching u(i+1) (all kept), and k pairs where the
// elevated e(d) needs u(d) and w(d) needs e(d) while watching u(n-1-d). Each
// w(d) watch fits alone but not with the chain, and its minimal conflict is
// the whole chain segment between u(d) and u(n-1-d).
func watchChainPlan(n, k int) []plan.Op {
	ops := []plan.Op{orderHdr}
	u := func(i int) string { return fmt.Sprint("u", i) }
	for i := range n {
		var watch []string
		if i+1 < n {
			watch = []string{u(i + 1)}
		}
		op := orderOp(u(i), false)
		op.IfChanged, op.Watch = len(watch) > 0, watch
		ops = append(ops, op)
	}
	for d := range k {
		e := fmt.Sprint("e", d)
		ops = append(ops, orderOp(e, true, u(d)), watchOp(fmt.Sprint("w", d), false, []string{u(n - 1 - d)}, e))
	}
	return ops
}

// TestOrderForPrivilegeSplitLongWatchChainIsFast guards against the
// regression where the minimal conflict search on long watch chains reran a
// separate deletion trial that rebuilt the watch graph for every dropped
// watch (5000 chain ops with 50 dropped watches took 5m32s); a chain of
// plain watches is now one run for shrinkConflict. Rather than an absolute
// wall-clock bound (flaky under machine load), it measures the search at a
// base chain length and at 4x that length and asserts the time grows well
// below quadratic (see assertGrowsSubQuadratically). Only the chain length
// is scaled: the search cost per dropped watch is roughly the chain length
// it walks, so with a fixed number of dropped watches k the total cost is
// linear in the chain length alone; scaling k along with it would make even
// this correct, O(chain length x k) algorithm look quadratic in the chain
// length, and wrongly fail the check. The base size alone was observed to
// take 10+ minutes per run on the pre-fix fixpoint solver, well past
// assertGrowsSubQuadratically's per-sample hang guard, so that guard (not
// Go's own test-binary timeout) is what catches a reintroduced regression
// here.
func TestOrderForPrivilegeSplitLongWatchChainIsFast(t *testing.T) {
	const k = 50
	check := func(n int) func() error {
		ops := watchChainPlan(n, k)
		wantConflict := n - 1
		wantKey := watchKey{"w0", fmt.Sprint("u", n-1)}
		return func() error {
			_, conflicts, err := orderForPrivilegeSplit(ops)
			if err != nil || len(conflicts) != k {
				return fmt.Errorf("orderForPrivilegeSplit() = %v, %d together-conflicts; want %d", err, len(conflicts), k)
			}
			// w0 watches the far end of the chain while e0 needs the near
			// end: the conflict must list every chain watch in between.
			if got := len(conflicts[wantKey]); got != wantConflict {
				return fmt.Errorf("conflict of w0 lists %d chain watches, want all %d", got, wantConflict)
			}
			return nil
		}
	}
	assertGrowsSubQuadratically(t,
		fmt.Sprintf("watchChainPlan(%d ops, %d watches)", 1250, k), check(1250),
		fmt.Sprintf("watchChainPlan(%d ops, %d watches)", 5000, k), check(5000))
}

func BenchmarkOrderForPrivilegeSplitRefusedWatches(b *testing.B) {
	ops := refusedWatchPlan(10000, 3000)
	for b.Loop() {
		if _, _, err := orderForPrivilegeSplit(ops); err != nil {
			b.Fatal(err)
		}
	}
}
