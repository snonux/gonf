package api

import (
	"container/heap"
	"fmt"
	"strings"

	"github.com/snonux/gonf/plan"
)

// orderForPrivilegeSplit returns the ops of an api.Apply plan that has
// elevated ops in an order that plan.SplitPrivilegeChunks can cut into
// privilege chunks without a dependency pointing into a later chunk and
// without a change-gated op landing in another chunk than a resource it
// watches, and with as few chunks (elevation round trips) as those two rules
// allow.
//
// Apply's op order carries no meaning of its own: RegisteredPlanDrafts
// returns the drafts sorted by resource ID, and within one chunk plan.Apply
// reorders by deps anyway. Split in that ID order, an unprivileged File that
// an elevated Command depends on could land in a later chunk merely because
// "Command[" sorts before "File[", and the pre-flight would refuse a valid
// recipe. So Apply, unlike Run (whose chunk order is the recorded task
// order), chooses the chunk order itself. It gives every op a chunk level
// (chunkLevels): the lowest level of its privilege class that is not below
// any dependency's level (strictly above it for a dependency of the other
// class), with a change-gated op and each same-class op it watches at one
// common level, since change reports do not cross chunks. That least
// assignment is the pointwise lowest one satisfying all rules, so its highest
// level, and with it the chunk count, is the minimum for its starting class
// (the class of level 0). Both starting classes are tried and the order with
// fewer chunks is kept; ties keep the class of the lowest-indexed op without
// deps, so the result is deterministic. The ops are then emitted level by
// level in dependency order (levelOrder).
//
// Not every watch can be kept. One across privilege classes never shares a
// chunk; one whose ends are forced apart by dependencies on the other class
// (A watches B, A needs an elevated E that needs B) cannot either; and some
// watches can each be kept alone but not together. keptWatches keeps them
// greedily in declaration order, so a dropped watch conflicts with the
// dependencies alone or together with watches kept before it. Only dropped
// watches can end up crossing chunks, and the pre-flight refusal
// (validateApplyDeps) names one of them, with the kept watches it conflicts
// with (the returned watchConflicts) when it would fit on its own.
//
// ops[0] is the plan header and stays first. Apply lowers registered
// resources to a flat op list (no when_begin/when_end), so the whole body is
// one sortable run. Deps naming no op of the plan are ignored here; the
// pre-flight refuses them as dangling. A dependency cycle, including an op
// depending on itself, is an error naming the cycle ("A -> B -> A", or
// "A -> A"): the elevated chunk is a separate root process, so the plan must
// be refused before ANY chunk applies, not by the engine once a later chunk
// is reached.
func orderForPrivilegeSplit(ops []plan.Op) ([]plan.Op, watchConflicts, error) {
	if len(ops) < 2 {
		return ops, nil, nil
	}
	body := ops[1:]
	g := newDepGraph(body)
	if _, ok := g.topoOrder(); !ok {
		return nil, nil, g.cycleError(body)
	}
	// Which watches can be kept does not depend on the starting class
	// (see watchesSatisfiable), so both starts share one kept set.
	kept, conflicts := g.keptWatches(body)
	first := body[g.firstReady()].Elevate
	order := g.levelOrder(g.chunkLevels(body, first, kept))
	if other := g.levelOrder(g.chunkLevels(body, !first, kept)); chunkCount(body, other) < chunkCount(body, order) {
		order = other
	}
	out := make([]plan.Op, 0, len(ops))
	out = append(out, ops[0])
	for _, i := range order {
		out = append(out, body[i])
	}
	return out, conflicts, nil
}

// depGraph is the in-plan dependency graph of an Apply body, by body index.
// A dep matching no op ID in the body adds no edge. A dep on the op's own ID
// IS an edge (a self-loop): it is the smallest dependency cycle, and the
// plan engine refuses it as circular too, so dropping it here would let the
// elevated chunk run as root before a later chunk hit that refusal.
type depGraph struct {
	byID    map[string]int // op ID → its first body index
	deps    [][]int        // op → the ops it depends on
	waiters [][]int        // op → the ops depending on it
}

func newDepGraph(body []plan.Op) depGraph {
	g := depGraph{
		byID:    make(map[string]int, len(body)),
		deps:    make([][]int, len(body)),
		waiters: make([][]int, len(body)),
	}
	for i, op := range body {
		if _, seen := g.byID[op.ID]; op.ID != "" && !seen {
			g.byID[op.ID] = i
		}
	}
	for i, op := range body {
		for _, dep := range op.Deps {
			if at, found := g.byID[dep]; found {
				g.deps[i] = append(g.deps[i], at)
				g.waiters[at] = append(g.waiters[at], i)
			}
		}
	}
	return g
}

// indegrees returns a fresh count of in-plan deps per op.
func (g depGraph) indegrees() []int {
	indeg := make([]int, len(g.deps))
	for i, d := range g.deps {
		indeg[i] = len(d)
	}
	return indeg
}

// firstReady returns the lowest index of an op without in-plan deps, or -1.
func (g depGraph) firstReady() int {
	for i, d := range g.deps {
		if len(d) == 0 {
			return i
		}
	}
	return -1
}

// topoOrder returns the body indexes in a dependency order (Kahn, lowest
// index first), or ok=false when a cycle leaves ops that never get ready.
func (g depGraph) topoOrder() (order []int, ok bool) {
	return g.kahn(func(int) int { return 0 })
}

// levelOrder emits the ops by ascending chunk level, in dependency order
// within a level (ties by lowest index). Every dep sits at a level no higher
// than its dependent, so taking the ready op of the lowest level never emits
// a level before a lower one is complete.
func (g depGraph) levelOrder(level []int) []int {
	order, _ := g.kahn(func(i int) int { return level[i] })
	return order
}

// kahn is a topological sort that always takes the ready op with the lowest
// (rank, index). It returns ok=false on a cycle. The ready ops sit in a heap
// ordered by (rank, index), so a plan of n ops and E deps sorts in
// O((n + E) log n); rank must not change while kahn runs.
func (g depGraph) kahn(rank func(int) int) (order []int, ok bool) {
	indeg := g.indegrees()
	ready := &readyHeap{rank: rank}
	for i, d := range indeg {
		if d == 0 {
			ready.ops = append(ready.ops, i)
		}
	}
	heap.Init(ready)
	order = make([]int, 0, len(g.deps))
	for ready.Len() > 0 {
		next := heap.Pop(ready).(int)
		order = append(order, next)
		for _, w := range g.waiters[next] {
			if indeg[w]--; indeg[w] == 0 {
				heap.Push(ready, w)
			}
		}
	}
	if len(order) < len(g.deps) {
		return nil, false // every remaining op waits on another: a cycle
	}
	return order, true
}

// readyHeap is kahn's min-heap of ready op indexes by (rank, index).
type readyHeap struct {
	ops  []int
	rank func(int) int
}

func (h *readyHeap) Len() int { return len(h.ops) }
func (h *readyHeap) Less(a, b int) bool {
	ra, rb := h.rank(h.ops[a]), h.rank(h.ops[b])
	return ra < rb || (ra == rb && h.ops[a] < h.ops[b])
}
func (h *readyHeap) Swap(a, b int) { h.ops[a], h.ops[b] = h.ops[b], h.ops[a] }
func (h *readyHeap) Push(x any)    { h.ops = append(h.ops, x.(int)) }
func (h *readyHeap) Pop() any {
	last := h.ops[len(h.ops)-1]
	h.ops = h.ops[:len(h.ops)-1]
	return last
}

// chunkCount is the number of privilege chunks order splits into: one plus
// the number of class changes along it (nil counts as no chunks).
func chunkCount(body []plan.Op, order []int) int {
	if len(order) == 0 {
		return 0
	}
	n := 1
	for k := 1; k < len(order); k++ {
		if body[order[k]].Elevate != body[order[k-1]].Elevate {
			n++
		}
	}
	return n
}

// cycleError names one dependency cycle of body as "Apply: circular
// dependency: A -> B -> A", where each op depends on the next. It walks deps
// from the lowest-indexed op still blocked after a Kahn pass (every such op
// has a blocked dep, so the walk must revisit an op) and reports the loop.
func (g depGraph) cycleError(body []plan.Op) error {
	indeg := g.indegrees()
	for changed := true; changed; { // peel off everything that is not blocked
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
	at, seen, path := -1, map[int]int{}, []int(nil)
	for i, n := range indeg {
		if n > 0 {
			at = i
			break
		}
	}
	for at >= 0 {
		if start, ok := seen[at]; ok {
			return cycleErrorFor(body, append(path[start:], at))
		}
		seen[at] = len(path)
		path = append(path, at)
		at = g.blockedDep(at, indeg)
	}
	return fmt.Errorf("Apply: circular dependency among the registered resources")
}

// blockedDep returns a dep of op that is itself still blocked, or -1.
func (g depGraph) blockedDep(op int, indeg []int) int {
	for _, d := range g.deps[op] {
		if indeg[d] > 0 {
			return d
		}
	}
	return -1
}

func cycleErrorFor(body []plan.Op, loop []int) error {
	ids := make([]string, len(loop))
	for k, i := range loop {
		ids[k] = body[i].ID
	}
	return fmt.Errorf("Apply: circular dependency: %s (each depends on the next); "+
		"refused before anything is applied", strings.Join(ids, " -> "))
}
