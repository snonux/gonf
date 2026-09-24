package api

import (
	"container/heap"
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/plan"
)

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

// readyHeap is kahn's min-heap of ready op indexes by (rank, index).
type readyHeap struct {
	ops  []int
	rank func(int) int
}

// newDepGraph builds body's in-plan dependency graph (see depGraph).
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
// deps, so the result is deterministic. Dependencies here are the recorded
// deps plus the parent-directory edges inferred from the ops' paths
// (withParentDirDeps), so an op inside a directory another op creates lands
// in its chunk or a later one without a DependsOn. The ops are then emitted
// level by level in dependency order (levelOrder).
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
//
// Cost, for n ops, E deps and W same-class watches: the order as a whole is
// NOT linear. The cycle check (cycleBlocked) and levelOrder are Kahn passes
// over a heap, O((n + E) log n); a refused cycle's walk (cycleError) and
// chunkLevels are O(n + E); keptWatches is
// O(W * (n + E)) in the worst case, and near linear on real plans (see
// there).
func orderForPrivilegeSplit(ops []plan.Op) ([]plan.Op, watchConflicts, error) {
	if len(ops) < 2 {
		return ops, nil, nil
	}
	body := ops[1:]
	g := newDepGraph(body)
	if blocked, acyclic := g.cycleBlocked(); !acyclic {
		return nil, nil, g.cycleError(body, blocked)
	}
	// Which watches can be kept does not depend on the starting class
	// (see watchesSatisfiable), so both starts share one kept set.
	kept, conflicts := g.keptWatches(body)
	g = g.withParentDirDeps(body, kept)
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

// withParentDirDeps returns g plus the parent-directory edges inferred from
// the ops' paths (plan.InferParentDirDeps): an op creating something inside
// a directory another op creates is placed after it, even across privilege
// classes (an elevated Dir before an unprivileged File inside it), so the
// chunk levels order it like a DependsOn. The inference sees the dependency
// edges AND both directions of every kept watch, and accepts an edge only if
// it closes no cycle there: no new strongly connected component forms, so the
// kept watches stay satisfiable (chunkLevels) and the Kahn passes see an
// acyclic graph, exactly as without the inferred edges. It runs after the
// cycle check and keptWatches, which therefore see the recorded deps only:
// a cycle refusal and the kept watch set (and so every pre-flight refusal)
// are unchanged by inference. g itself is not modified.
func (g depGraph) withParentDirDeps(body []plan.Op, kept []watchPair) depGraph {
	lg := newLevelGraph(g, body, kept)
	edges := plan.InferParentDirDeps(body, lg.each)
	if len(edges) == 0 {
		return g
	}
	out := depGraph{byID: g.byID, deps: cloneAdjacency(g.deps), waiters: cloneAdjacency(g.waiters)}
	for _, e := range edges {
		out.deps[e.Dependent] = append(out.deps[e.Dependent], e.Dep)
		out.waiters[e.Dep] = append(out.waiters[e.Dep], e.Dependent)
	}
	return out
}

// cloneAdjacency deep-copies an adjacency list, so appending to the copy
// never writes into the original's backing arrays.
func cloneAdjacency(adj [][]int) [][]int {
	out := make([][]int, len(adj))
	for i, a := range adj {
		out[i] = slices.Clone(a)
	}
	return out
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

// cycleBlocked runs one Kahn pass and reports whether it emitted every op
// (acyclic). When it did not, blocked is that pass's final in-degree count:
// > 0 exactly for the ops a dependency cycle keeps from ever getting ready
// (the cycle's ops and everything depending on them), counting each such
// op's deps that are blocked too; 0 for every emitted op.
func (g depGraph) cycleBlocked() (blocked []int, acyclic bool) {
	order, indeg := g.kahn(func(int) int { return 0 })
	return indeg, len(order) == len(g.deps)
}

// levelOrder emits the ops by ascending chunk level, in dependency order
// within a level (ties by lowest index). Every dep sits at a level no higher
// than its dependent, so taking the ready op of the lowest level never emits
// a level before a lower one is complete. It returns nil on a cycle, which
// its callers have already refused (cycleBlocked).
func (g depGraph) levelOrder(level []int) []int {
	order, _ := g.kahn(func(i int) int { return level[i] })
	if len(order) < len(g.deps) {
		return nil
	}
	return order
}

// kahn is a topological sort that always takes the ready op with the lowest
// (rank, index). On a cycle order is short: it misses every op the cycle
// blocks. indeg is the pass's final in-degree count, 0 for every emitted op
// and, for a blocked one, the number of its deps that were never emitted
// (see cycleBlocked). The ready ops sit in a heap ordered by (rank, index),
// so a plan of n ops and E deps sorts in O((n + E) log n); rank must not
// change while kahn runs.
func (g depGraph) kahn(rank func(int) int) (order, indeg []int) {
	indeg = g.indegrees()
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
	return order, indeg
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
// dependency: A -> B -> A", where each op depends on the next. blocked is
// cycleBlocked's final in-degree count. It walks deps from the lowest-indexed
// blocked op (every blocked op has a blocked dep, so the walk must revisit an
// op) and reports the loop. Each op is visited at most once and each of its
// deps scanned at most once, so the refusal costs O(n + E) on top of the
// Kahn pass that found the cycle.
func (g depGraph) cycleError(body []plan.Op, blocked []int) error {
	at, seen, path := -1, map[int]int{}, []int(nil)
	for i, n := range blocked {
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
		at = g.blockedDep(at, blocked)
	}
	return fmt.Errorf("Apply: circular dependency among the registered resources")
}

// blockedDep returns a dep of op that is itself still blocked (blocked[d] >
// 0, see cycleBlocked), or -1.
func (g depGraph) blockedDep(op int, blocked []int) int {
	for _, d := range g.deps[op] {
		if blocked[d] > 0 {
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
