package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/snonux/gonf/plan"
)

var orderHdr = plan.Op{Op: plan.KindPlan, Version: plan.CurrentVersion, ID: "apply"}

// orderOp builds a command op for the ordering tests.
func orderOp(id string, elevate bool, deps ...string) plan.Op {
	return plan.Op{Op: plan.KindCommand, Bin: "true", ID: id, Elevate: elevate, Deps: deps}
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

// refusedWatchPlan builds a plan of n ops with w watches that no order can
// keep (each gated op needs an elevated op that needs what it watches),
// padded with a dependency chain alternating privilege classes.
func refusedWatchPlan(n, w int) []plan.Op {
	ops := []plan.Op{orderHdr}
	for k := range w {
		b, e, a := fmt.Sprint("b", k), fmt.Sprint("e", k), fmt.Sprint("a", k)
		ops = append(ops, orderOp(b, false), orderOp(e, true, b), watchOp(a, false, []string{b}, e, b))
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

// TestOrderForPrivilegeSplitLargeRefusedPlanIsFast bounds the time of the
// worst case the review measured at 5.6s for the old fixpoint solver: 2000
// ops with 300 watches that must all be dropped. Each satisfiability check
// is linear now, so this takes tens of milliseconds; the race detector
// slows it about tenfold, so the bound is scaled under -race.
func TestOrderForPrivilegeSplitLargeRefusedPlanIsFast(t *testing.T) {
	ops := refusedWatchPlan(2000, 300)
	bound := 500 * time.Millisecond
	if raceEnabled {
		bound *= 10
	}
	start := time.Now()
	_, conflicts, err := orderForPrivilegeSplit(ops)
	if elapsed := time.Since(start); elapsed > bound {
		t.Fatalf("orderForPrivilegeSplit took %v for 2000 ops / 300 refused watches, want < %v", elapsed, bound)
	}
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("orderForPrivilegeSplit() = %v, %v; want no error and no together-conflicts", err, conflicts)
	}
}

func BenchmarkOrderForPrivilegeSplitRefusedWatches(b *testing.B) {
	ops := refusedWatchPlan(2000, 300)
	for b.Loop() {
		if _, _, err := orderForPrivilegeSplit(ops); err != nil {
			b.Fatal(err)
		}
	}
}
