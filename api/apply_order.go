package api

import (
	"fmt"
	"strings"

	"github.com/snonux/gonf/plan"
)

// orderForPrivilegeSplit returns the ops of an api.Apply plan that has
// elevated ops in an order that plan.SplitPrivilegeChunks can cut into
// privilege chunks without a dependency pointing into a later chunk, and
// with as few chunks (elevation round trips) as the dependency graph allows.
//
// Apply's op order carries no meaning of its own: RegisteredPlanDrafts
// returns the drafts sorted by resource ID, and within one chunk plan.Apply
// reorders by deps anyway. Split in that ID order, an unprivileged File that
// an elevated Command depends on could land in a later chunk merely because
// "Command[" sorts before "File[", and the pre-flight would refuse a valid
// recipe. So Apply, unlike Run (whose chunk order is the recorded task
// order), chooses the chunk order itself: a dependency sort that keeps each
// privilege class together (sameClassKahn), tried once starting with each
// class, keeping the order with fewer chunks. For two classes this greedy is
// optimal for its starting class: emitting every ready op of the current
// class before switching only removes ops from what is left, which never
// needs more chunks. Ties keep the order that starts with the class of the
// lowest-indexed ready op, so the result is deterministic.
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
	ready := g.firstReady()
	if ready < 0 { // no op without deps: everything sits on a cycle
		return nil, g.cycleError(body)
	}
	first := body[ready].Elevate
	order, ok := sameClassKahn(body, g, first)
	if !ok {
		return nil, g.cycleError(body)
	}
	if other, _ := sameClassKahn(body, g, !first); chunkCount(body, other) < chunkCount(body, order) {
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
	deps    [][]int // op → the ops it depends on
	waiters [][]int // op → the ops depending on it
}

func newDepGraph(body []plan.Op) depGraph {
	byID := make(map[string]int, len(body))
	for i, op := range body {
		if op.ID != "" {
			byID[op.ID] = i
		}
	}
	g := depGraph{deps: make([][]int, len(body)), waiters: make([][]int, len(body))}
	for i, op := range body {
		for _, dep := range op.Deps {
			if at, found := byID[dep]; found {
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

// sameClassKahn topologically orders body and returns the body indexes in
// that order, or ok=false on a cycle. It starts with class (Elevate) and,
// among the ready ops, takes the lowest-indexed one of the class emitted
// last, switching class only when no op of that class is ready.
func sameClassKahn(body []plan.Op, g depGraph, class bool) (order []int, ok bool) {
	indeg := g.indegrees()
	emitted := make([]bool, len(body))
	order = make([]int, 0, len(body))
	for len(order) < len(body) {
		next := pickReady(body, indeg, emitted, class)
		if next < 0 {
			return nil, false // every remaining op waits on another: a cycle
		}
		emitted[next] = true
		class = body[next].Elevate
		order = append(order, next)
		for _, w := range g.waiters[next] {
			indeg[w]--
		}
	}
	return order, true
}

// pickReady returns the lowest-indexed ready op (no unmet deps, not yet
// emitted) of class, falling back to the lowest-indexed ready op of the other
// class when none of class is ready. It returns -1 when no op is ready.
func pickReady(body []plan.Op, indeg []int, emitted []bool, class bool) int {
	fallback := -1
	for i := range body {
		if emitted[i] || indeg[i] > 0 {
			continue
		}
		if body[i].Elevate == class {
			return i
		}
		if fallback < 0 {
			fallback = i
		}
	}
	return fallback
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
