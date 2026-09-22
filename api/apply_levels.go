package api

import (
	"slices"

	"github.com/snonux/gonf/plan"
)

// watchPair is a change-gated op (gated) watching an op of its own privilege
// class (watched), by body index.
type watchPair struct{ gated, watched int }

// watchKey names a watch by resource IDs, as the pre-flight sees it.
type watchKey struct{ gated, watched string }

// watchConflicts maps a watch that keptWatches dropped although it would fit
// on its own to a minimal set of kept watches it conflicts with, each
// described as "<gated> watching <watched>": together with them it cannot
// fit, and without any one of them it could. A dropped watch that cannot fit
// even alone (its ends forced apart by dependencies) has no entry.
type watchConflicts map[watchKey][]string

// levelGraph is the constraint graph of the chunk levels. Write an op's
// level as 2*x + b, where b is 0 for the starting class and 1 for the other.
// A dependency edge u -> v then asks x[v] >= x[u], plus one when u is of the
// other class and v of the starting class; a watch asks x equal at both ends
// (an edge each way). This is a difference-constraint system with weights 0
// and 1, so it is solvable iff no strongly connected component contains a
// weight-1 edge, and its least solution is the longest path over the
// components. The dependency edges (waiters, shared with depGraph and never
// modified) and the watch edges are kept apart, so trying another watch set
// only builds the few watch edges. seen and back are scratch space for the
// forward and backward searches (see pathMarks), shared by the graphs derived
// from one another (withWatches).
type levelGraph struct {
	body     []plan.Op
	waiters  [][]int       // dependency edges u -> v, from depGraph
	incoming [][]int       // the same edges by target: v -> its deps
	watch    map[int][]int // watch edges, both directions
	seen     *pathMarks    // forward search scratch
	back     *pathMarks    // backward search scratch
}

// tarjan is the state of one strongly-connected-components pass.
type tarjan struct {
	lg          levelGraph
	index, low  []int
	onStack     []bool
	stack       []int
	comp        []int
	next, count int
}

func newLevelGraph(g depGraph, body []plan.Op, watches []watchPair) levelGraph {
	n := len(body)
	lg := levelGraph{body: body, waiters: g.waiters, incoming: g.deps,
		seen: newPathMarks(n), back: newPathMarks(n)}
	return lg.withWatches(watches)
}

// withWatches is lg with exactly the given watch edges (dependencies and
// scratch space shared).
func (lg levelGraph) withWatches(watches []watchPair) levelGraph {
	lg.watch = make(map[int][]int, 2*len(watches))
	for _, p := range watches {
		lg.addWatch(p)
	}
	return lg
}

// addWatch adds the two edges of watch p.
func (lg levelGraph) addWatch(p watchPair) {
	lg.watch[p.gated] = append(lg.watch[p.gated], p.watched)
	lg.watch[p.watched] = append(lg.watch[p.watched], p.gated)
}

// removeWatch removes one copy of each of the two edges of watch p.
func (lg levelGraph) removeWatch(p watchPair) {
	drop := func(u, v int) {
		if i := slices.Index(lg.watch[u], v); i >= 0 {
			lg.watch[u] = slices.Delete(lg.watch[u], i, i+1)
		}
	}
	drop(p.gated, p.watched)
	drop(p.watched, p.gated)
}

// each calls fn for every edge u -> v leaving u.
func (lg levelGraph) each(u int, fn func(v int)) {
	for _, v := range lg.waiters[u] {
		fn(v)
	}
	for _, v := range lg.watch[u] {
		fn(v)
	}
}

// keptWatches returns the same-class watches chunkLevels keeps: all of them
// when they fit together, otherwise greedily in declaration order, each one
// only if it still fits together with the watches kept before it. It also
// returns, for every dropped watch that would fit on its own, a minimal set
// of kept watches it conflicts with.
//
// The greedy step asks whether one more watch breaks a satisfiable graph,
// which closesCrossCycle answers by searches between the watch's ends
// instead of a full component pass. Each costs O(R), R being the smaller of
// what one end reaches forward and the other backward (hasCrossWalk), so the
// greedy costs O(W * R) for W watches: O(W * (n + E)) in the worst case,
// where every watch's ends both reach most of the plan, and far less on real
// plans (a chain of watches grows at a fresh op, R about 1). conflictsOf
// then explains each dropped watch; see its cost there.
func (g depGraph) keptWatches(body []plan.Op) ([]watchPair, watchConflicts) {
	all := g.sameClassWatches(body)
	if _, ok := newLevelGraph(g, body, all).watchesSatisfiable(); ok {
		return all, nil
	}
	lg := newLevelGraph(g, body, nil)
	kept := make([]watchPair, 0, len(all))
	var dropped []watchPair
	for _, p := range all {
		if lg.closesCrossCycle(p) {
			dropped = append(dropped, p)
			continue
		}
		kept = append(kept, p)
		lg.addWatch(p)
	}
	return kept, conflictsOf(lg, kept, dropped)
}

// chunkLevels assigns every op the chunk level described at
// orderForPrivilegeSplit, level 0 having class start (and odd levels the
// other class), honouring the dependencies and the kept watches (which must
// be satisfiable, as keptWatches guarantees). It runs in linear time: one
// component pass and one longest-path pass over the components.
func (g depGraph) chunkLevels(body []plan.Op, start bool, kept []watchPair) []int {
	lg := newLevelGraph(g, body, kept)
	comp, count := lg.components()
	b := func(i int) int {
		if body[i].Elevate == start {
			return 0
		}
		return 1
	}
	members := make([][]int, count)
	for i, c := range comp {
		members[c] = append(members[c], i)
	}
	x := make([]int, count)
	for c := count - 1; c >= 0; c-- { // components completes sinks first: count-1 is a source
		for _, u := range members[c] {
			lg.each(u, func(v int) {
				if comp[v] != c {
					x[comp[v]] = max(x[comp[v]], x[c]+b(u)*(1-b(v)))
				}
			})
		}
	}
	level := make([]int, len(body))
	for i := range body {
		level[i] = 2*x[comp[i]] + b(i)
	}
	return level
}

// watchesSatisfiable reports whether the levels can meet every rule of lg,
// and otherwise the component holding a cross-class edge. It is independent
// of the starting class: every cycle crosses from one class to the other as
// often as back, so a component with a cross-class edge has one of weight 1
// for either start.
func (lg levelGraph) watchesSatisfiable() (badComponent int, ok bool) {
	comp, _ := lg.components()
	bad := -1
	for u := range lg.body {
		lg.each(u, func(v int) {
			if bad < 0 && comp[u] == comp[v] && lg.body[u].Elevate != lg.body[v].Elevate {
				bad = comp[u]
			}
		})
	}
	return bad, bad < 0
}

// components numbers the strongly connected components of lg (Tarjan). A
// component is numbered when it completes, and edges only lead to components
// completed earlier, so higher numbers come first in dependency order.
func (lg levelGraph) components() (comp []int, count int) {
	n := len(lg.body)
	t := tarjan{lg: lg, index: make([]int, n), low: make([]int, n), onStack: make([]bool, n), comp: make([]int, n)}
	for i := range t.index {
		t.index[i] = -1
	}
	for i := range n {
		if t.index[i] < 0 {
			t.visit(i)
		}
	}
	return t.comp, t.count
}

func (t *tarjan) visit(u int) {
	t.index[u], t.low[u] = t.next, t.next
	t.next++
	t.stack = append(t.stack, u)
	t.onStack[u] = true
	for _, v := range t.lg.waiters[u] {
		t.edge(u, v)
	}
	for _, v := range t.lg.watch[u] {
		t.edge(u, v)
	}
	if t.low[u] != t.index[u] {
		return
	}
	for {
		w := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		t.onStack[w] = false
		t.comp[w] = t.count
		if w == u {
			break
		}
	}
	t.count++
}

// edge follows u -> v during visit (the hot path, so not through each).
func (t *tarjan) edge(u, v int) {
	if t.index[v] < 0 {
		t.visit(v)
		t.low[u] = min(t.low[u], t.low[v])
	} else if t.onStack[v] {
		t.low[u] = min(t.low[u], t.index[v])
	}
}

// sameClassWatches pairs every change-gated op with each op of its own
// privilege class that it watches, in declaration order. Watches across
// classes or naming no op of the plan are not paired: no order can satisfy
// them, and the pre-flight refuses them.
func (g depGraph) sameClassWatches(body []plan.Op) []watchPair {
	var pairs []watchPair
	for i, op := range body {
		if !op.IfChanged {
			continue
		}
		for _, w := range op.Watch {
			if j, found := g.byID[w]; found && j != i && body[j].Elevate == op.Elevate {
				pairs = append(pairs, watchPair{gated: i, watched: j})
			}
		}
	}
	return pairs
}
