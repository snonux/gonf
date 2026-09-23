package api

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/snonux/gonf/plan"
)

// peelBlocked is the fixed-point peel cycleError used before it took
// cycleBlocked's Kahn in-degrees: repeatedly retire every op whose deps are
// all retired. It is kept here as the reference the linear walk must match.
func peelBlocked(g depGraph) []int {
	indeg := g.indegrees()
	for changed := true; changed; {
		changed = false
		for i := range indeg {
			if indeg[i] == 0 {
				indeg[i] = -1
				changed = true
				for _, w := range g.waiters[i] {
					indeg[w]--
				}
			}
		}
	}
	return indeg
}

// randomCyclicBody builds n ops with random deps (some self-deps, some
// dangling), dense enough that most bodies hold a cycle.
func randomCyclicBody(r *rand.Rand, n int) []plan.Op {
	body := make([]plan.Op, n)
	for i := range body {
		body[i] = plan.Op{ID: fmt.Sprintf("Command[%d]", i), Elevate: r.IntN(2) == 0}
		for range r.IntN(3) {
			body[i].Deps = append(body[i].Deps, fmt.Sprintf("Command[%d]", r.IntN(n+1)))
		}
	}
	return body
}

// TestCycleErrorMatchesPeel pins that cycleError names the same cycle from
// cycleBlocked's Kahn in-degrees as it did from the old fixed-point peel:
// the blocked sets agree op for op, and so does the refusal text.
func TestCycleErrorMatchesPeel(t *testing.T) {
	r := rand.New(rand.NewPCG(62, 2))
	cyclic := 0
	for range 20000 {
		body := randomCyclicBody(r, 1+r.IntN(8))
		g := newDepGraph(body)
		blocked, acyclic := g.cycleBlocked()
		ref := peelBlocked(g)
		for i := range ref {
			if (ref[i] > 0) != (blocked[i] > 0) {
				t.Fatalf("op %d: blocked %d, peel %d (body %+v)", i, blocked[i], ref[i], body)
			}
		}
		if acyclic {
			continue
		}
		cyclic++
		got, want := g.cycleError(body, blocked).Error(), g.cycleError(body, ref).Error()
		if got != want {
			t.Fatalf("cycleError = %q, peel gives %q (body %+v)", got, want, body)
		}
	}
	if cyclic == 0 {
		t.Fatal("no cyclic body generated")
	}
}

// chainBeforeCycle builds the shape that made the old peel quadratic: n
// unprivileged ops File[0..n-1], each depending on the NEXT one (so a
// low-to-high peel pass retired only one of them), followed by an x <-> y
// cycle.
func chainBeforeCycle(n int) []plan.Op {
	body := make([]plan.Op, 0, n+2)
	for i := range n {
		var deps []string
		if i+1 < n {
			deps = []string{fmt.Sprintf("File[%d]", i+1)}
		}
		body = append(body, orderOp(fmt.Sprintf("File[%d]", i), false, deps...))
	}
	return append(body, orderOp("Command[x]", false, "Command[y]"), orderOp("Command[y]", false, "Command[x]"))
}

// TestCycleErrorLongChainBeforeCycle pins that chainBeforeCycle's refusal
// names just the cycle, the chain being unblocked, and guards against a
// quadratic or worse regression in getting there: the old fixed-point peel
// (peelBlocked) took hundreds of milliseconds at 40k ops on this shape
// because each low-to-high pass retired only one link of the chain. Checking
// the error text alone would not catch that regression coming back -- the
// old peel produces the same text, just slowly -- so, like the ab2/pb2 perf
// guards in api/apply_order_test.go, this also measures the same shape at a
// base size and at 4x that size and asserts the time grows well below
// quadratic (see assertGrowsSubQuadratically).
func TestCycleErrorLongChainBeforeCycle(t *testing.T) {
	want := "Apply: circular dependency: Command[x] -> Command[y] -> Command[x] (each depends on the next); " +
		"refused before anything is applied"
	check := func(n int) func() error {
		ops := append([]plan.Op{orderHdr}, chainBeforeCycle(n)...)
		return func() error {
			_, _, err := orderForPrivilegeSplit(ops)
			if err == nil || err.Error() != want {
				return fmt.Errorf("err = %v, want %q", err, want)
			}
			return nil
		}
	}
	assertGrowsSubQuadratically(t,
		fmt.Sprintf("chainBeforeCycle(%d)", 10000), check(10000),
		fmt.Sprintf("chainBeforeCycle(%d)", 40000), check(40000))
}
