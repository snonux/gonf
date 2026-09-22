package api

import (
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
// class), with a change-gated op and each same-class op it watches raised to
// one common level, since change reports do not cross chunks. That least
// assignment is the pointwise lowest one satisfying all rules, so its highest
// level, and with it the chunk count, is the minimum for its starting class
// (the class of level 0). Both starting classes are tried and the order with
// fewer chunks is kept; ties keep the class of the lowest-indexed op without
// deps, so the result is deterministic. The ops are then emitted level by
// level in dependency order (levelOrder).
//
// A watch across privilege classes can never share a chunk, and a watch
// whose two ends are forced apart by dependencies on the other class (A
// watches B, A needs an elevated E that needs B) cannot either. Only such
// unsatisfiable watches are dropped from the assignment (chunkLevels keeps
// every other one), so they are the ones that end up crossing chunks and the
// pre-flight refusal (validateApplyDeps) names one of them, never a watch
// that could have been kept.
//
// ops[0] is the plan header and stays first. Apply lowers registered
// resources to a flat op list (no when_begin/when_end), so the whole body is
// one sortable run. Deps naming no op of the plan are ignored here; the
// pre-flight refuses them as dangling. A dependency cycle, including an op
// depending on itself, is an error naming the cycle ("A -> B -> A", or
// "A -> A"): the elevated chunk is a separate root process, so the plan must
// be refused before ANY chunk applies, not by the engine once a later chunk
// is reached.
func orderForPrivilegeSplit(ops []plan.Op) ([]plan.Op, error) {
	if len(ops) < 2 {
		return ops, nil
	}
	body := ops[1:]
	g := newDepGraph(body)
	topo, ok := g.topoOrder()
	if !ok {
		return nil, g.cycleError(body)
	}
	first := body[g.firstReady()].Elevate
	order := g.levelOrder(g.chunkLevels(body, topo, first))
	if other := g.levelOrder(g.chunkLevels(body, topo, !first)); chunkCount(body, other) < chunkCount(body, order) {
		order = other
	}
	out := make([]plan.Op, 0, len(ops))
	out = append(out, ops[0])
	for _, i := range order {
		out = append(out, body[i])
	}
	return out, nil
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
// (rank, index). It returns ok=false on a cycle.
func (g depGraph) kahn(rank func(int) int) (order []int, ok bool) {
	indeg := g.indegrees()
	emitted := make([]bool, len(g.deps))
	order = make([]int, 0, len(g.deps))
	for len(order) < len(g.deps) {
		next := -1
		for i := range g.deps {
			if !emitted[i] && indeg[i] == 0 && (next < 0 || rank(i) < rank(next)) {
				next = i
			}
		}
		if next < 0 {
			return nil, false // every remaining op waits on another: a cycle
		}
		emitted[next] = true
		order = append(order, next)
		for _, w := range g.waiters[next] {
			indeg[w]--
		}
	}
	return order, true
}

// chunkLevels assigns every op the chunk level described at
// orderForPrivilegeSplit, level 0 having class start (and odd levels the
// other class). When the same-class watches cannot all be kept in one chunk,
// it keeps them greedily in watch order: a watch is added only if the kept
// set stays satisfiable. A subset of a satisfiable set is satisfiable, so
// the result is a maximal satisfiable set, and each dropped watch conflicts
// with the dependencies plus the kept watches; the pre-flight then names a
// dropped one (the only kind that can cross chunks) instead of an innocent
// watch that a drop-everything fallback would have split.
func (g depGraph) chunkLevels(body []plan.Op, topo []int, start bool) []int {
	watches := g.sameClassWatches(body)
	if level, ok := g.solveLevels(body, topo, start, watches); ok {
		return level
	}
	level, _ := g.solveLevels(body, topo, start, nil) // deps alone always solve
	kept := make([][2]int, 0, len(watches))
	for _, p := range watches {
		if l, ok := g.solveLevels(body, topo, start, append(kept, p)); ok {
			kept, level = append(kept, p), l
		}
	}
	return level
}

// solveLevels raises levels until every rule holds: each op at or above the
// level minLevel derives from its deps, and both ends of every watch pair at
// one level. It starts at 0 and only ever raises, so it reaches the least
// assignment. That one has no empty level between two used ones (dropping
// such a gap by two keeps every rule), so no level exceeds len(body) + 1;
// passing that bound means the watches contradict the dependencies
// (ok=false). Without watches one pass in topo order is enough.
func (g depGraph) solveLevels(body []plan.Op, topo []int, start bool, watches [][2]int) (level []int, ok bool) {
	level = make([]int, len(body))
	limit := len(body) + 1
	for changed := true; changed; {
		changed = false
		for _, i := range topo {
			if need := g.minLevel(body, level, i, start); need > level[i] {
				level[i], changed = need, true
			}
		}
		for _, p := range watches {
			if top := max(level[p[0]], level[p[1]]); level[p[0]] != level[p[1]] {
				level[p[0]], level[p[1]], changed = top, top, true
			}
		}
		for _, l := range level {
			if l > limit {
				return nil, false
			}
		}
	}
	return level, true
}

// minLevel is the lowest level op i may take given its deps' current levels:
// not below a same-class dep, above an other-class dep, and of i's own class.
func (g depGraph) minLevel(body []plan.Op, level []int, i int, start bool) int {
	need := level[i]
	for _, d := range g.deps[i] {
		at := level[d]
		if body[d].Elevate != body[i].Elevate {
			at++
		}
		need = max(need, at)
	}
	if levelClass(need, start) != body[i].Elevate {
		need++
	}
	return need
}

// levelClass is the privilege class (Elevate) of chunk level k.
func levelClass(k int, start bool) bool { return start != (k%2 == 1) }

// sameClassWatches pairs every change-gated op with each op of its own
// privilege class that it watches. Watches across classes or naming no op of
// the plan are not paired: no order can satisfy them, and the pre-flight
// refuses them.
func (g depGraph) sameClassWatches(body []plan.Op) [][2]int {
	var pairs [][2]int
	for i, op := range body {
		if !op.IfChanged {
			continue
		}
		for _, w := range op.Watch {
			if j, found := g.byID[w]; found && j != i && body[j].Elevate == op.Elevate {
				pairs = append(pairs, [2]int{i, j})
			}
		}
	}
	return pairs
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
