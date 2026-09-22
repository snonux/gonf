package api

import (
	"fmt"
	"slices"
	"strings"
)

// pathMarks is the scratch space of one search over (op, crossed) states
// (stateSearch), state 2*op+crossed: which states it visited (mark == stamp; bumping
// stamp clears all marks at once), and for each visited state the state it
// was reached from (parent) and whether through a watch edge (byWatch), so a
// found walk can be read back (witness).
type pathMarks struct {
	mark    []int
	parent  []int
	byWatch []bool
	stamp   int
}

func newPathMarks(ops int) *pathMarks {
	return &pathMarks{mark: make([]int, 2*ops), parent: make([]int, 2*ops), byWatch: make([]bool, 2*ops)}
}

// conflictsOf finds, for each dropped watch that fits on its own, a minimal
// set of kept watches it conflicts with; keptGraph holds the dependencies
// and every kept watch. It reads the kept watches off ONE walk that closes
// the conflict in keptGraph (crossPath's witness, a breadth-first walk, so
// usually a short one) and then removes every one the conflict does not
// need (shrinkConflict). The result is minimal: removing any listed watch
// would let the dropped one fit, so every watch named in the refusal is part
// of the conflict.
//
// Cost per dropped watch: the alone check (hasCrossWalk, O(R)), the witness
// search (forward only, O(F), F what p's ends reach forward in keptGraph)
// and shrinkConflict's U trials of O(R) each, U being the number of runs the
// witness's watches fall into (see seriesUnits), not the number of watches:
// a long chain of plain watches is one run and one trial. The worst case, a
// witness whose every op has dependencies, is O(L * (n + E)) for a witness
// of L watches.
func conflictsOf(keptGraph levelGraph, kept, dropped []watchPair) watchConflicts {
	body := keptGraph.body
	deps := keptGraph.withWatches(nil)
	index := keptIndex(kept)
	conflicts := watchConflicts{}
	for _, p := range dropped {
		if deps.closesCrossCycle(p) {
			continue // cannot fit even alone: no "together with" entry
		}
		with := keptGraph.witness(p, kept, index)
		with = shrinkConflict(deps, with, p)
		names := make([]string, len(with))
		for i, k := range with {
			names[i] = body[k.gated].ID + " watching " + body[k.watched].ID
		}
		conflicts[watchKey{body[p.gated].ID, body[p.watched].ID}] = names
	}
	return conflicts
}

// closesCrossCycle reports whether adding watch p to lg, whose watches are
// satisfiable, breaks it. A new component with a cross-class edge must close
// its cycle through one of p's two edges, so this is the case exactly when
// lg already has a walk between p's ends, in either direction, that uses a
// cross-class edge (hasCrossWalk).
func (lg levelGraph) closesCrossCycle(p watchPair) bool {
	return lg.hasCrossWalk(p.gated, p.watched) || lg.hasCrossWalk(p.watched, p.gated)
}

// hasCrossWalk reports whether lg has a walk from from to to that uses at
// least one cross-class edge. It runs a forward search from from and a
// backward search from to in lockstep, one state each per round, and stops
// as soon as either finds the other end or runs out of states, since each
// alone decides the question. It therefore costs O(min(forward reach,
// backward reach)): adding a watch to a fresh op of a long chain looks at
// that op, not at the whole chain.
func (lg levelGraph) hasCrossWalk(from, to int) bool {
	fwd := lg.forwardSearch(from, to)
	back := lg.backwardSearch(from, to)
	for {
		if done, found := fwd.step(); done {
			return found
		}
		if done, found := back.step(); done {
			return found
		}
	}
}

// crossPath is hasCrossWalk by the forward search alone, which records in
// lg.seen how each state was reached, for witness to read back.
func (lg levelGraph) crossPath(from, to int) bool {
	fwd := lg.forwardSearch(from, to)
	for {
		if done, found := fwd.step(); done {
			return found
		}
	}
}

// stateSearch is one breadth-first search over (op, crossed) states, state
// 2*op+crossed, advanced one state at a time by step.
type stateSearch struct {
	lg     levelGraph
	marks  *pathMarks
	queue  []int
	target int
	expand func(s *stateSearch, st int)
}

// forwardSearch walks edges forward from (from, not crossed) towards (to,
// crossed). A crossed state dominates the uncrossed one of the same op,
// which is then not visited.
func (lg levelGraph) forwardSearch(from, to int) *stateSearch {
	s := &stateSearch{lg: lg, marks: lg.seen, target: 2*to + 1, expand: expandForward}
	s.marks.stamp++
	s.visit(2*from, -1, false)
	return s
}

// backwardSearch walks edges backward from (to, crossed) towards (from, not
// crossed): a state (y, c) is one from which to can still be reached with a
// crossing done once c is 1.
func (lg levelGraph) backwardSearch(from, to int) *stateSearch {
	s := &stateSearch{lg: lg, marks: lg.back, target: 2 * from, expand: expandBackward}
	s.marks.stamp++
	s.visit(2*to+1, -1, false)
	return s
}

// step expands one queued state. It reports done when the target was taken
// from the queue (found) or the queue ran empty (not found).
func (s *stateSearch) step() (done, found bool) {
	if len(s.queue) == 0 {
		return true, false
	}
	st := s.queue[0]
	s.queue = s.queue[1:]
	if st == s.target {
		return true, true
	}
	s.expand(s, st)
	return false, false
}

// visit queues state st unless already seen, recording how it was reached.
func (s *stateSearch) visit(st, parent int, byWatch bool) {
	m := s.marks
	if m.mark[st] == m.stamp {
		return
	}
	m.mark[st], m.parent[st], m.byWatch[st] = m.stamp, parent, byWatch
	s.queue = append(s.queue, st)
}

func expandForward(s *stateSearch, st int) {
	lg, u, crossed := s.lg, st/2, st%2
	step := func(v int, byWatch bool) {
		c := crossed
		if lg.body[v].Elevate != lg.body[u].Elevate {
			c = 1
		}
		if s.marks.mark[2*v+1] != s.marks.stamp { // crossed dominates
			s.visit(2*v+c, st, byWatch)
		}
	}
	for _, v := range lg.waiters[u] {
		step(v, false)
	}
	for _, v := range lg.watch[u] {
		step(v, true)
	}
}

func expandBackward(s *stateSearch, st int) {
	lg, y, crossed := s.lg, st/2, st%2
	pred := func(x int) {
		if lg.body[x].Elevate == lg.body[y].Elevate {
			s.visit(2*x+crossed, st, false)
		} else if crossed == 1 { // the crossing happens on this edge
			s.visit(2*x, st, false)
			s.visit(2*x+1, st, false)
		}
	}
	for _, x := range lg.incoming[y] {
		pred(x)
	}
	for _, x := range lg.watch[y] {
		pred(x)
	}
}

// edgeKey is the undirected edge of a watch.
func edgeKey(k watchPair) [2]int { return [2]int{min(k.gated, k.watched), max(k.gated, k.watched)} }

// keptIndex maps each undirected watch edge to its first watch in kept.
func keptIndex(kept []watchPair) map[[2]int]int {
	index := make(map[[2]int]int, len(kept))
	for i, k := range kept {
		if _, dup := index[edgeKey(k)]; !dup {
			index[edgeKey(k)] = i
		}
	}
	return index
}

// witness returns the kept watches on one walk through which p closes a
// cross-class cycle in lg (which holds exactly the kept watches, indexed by
// keptIndex), in kept's order. p must close one (it was dropped).
func (lg levelGraph) witness(p watchPair, kept []watchPair, index map[[2]int]int) []watchPair {
	from, to := p.watched, p.gated
	if !lg.crossPath(from, to) {
		from, to = p.gated, p.watched
		lg.crossPath(from, to)
	}
	m := lg.seen
	var used []int
	for st := 2*to + 1; m.parent[st] >= 0; st = m.parent[st] {
		if m.byWatch[st] {
			used = append(used, index[edgeKey(watchPair{m.parent[st] / 2, st / 2})])
		}
	}
	slices.Sort(used)
	used = slices.Compact(used)
	out := make([]watchPair, len(used))
	for i, k := range used {
		out[i] = kept[k]
	}
	return out
}

// shrinkConflict removes from with every watch the conflict of p does not
// need, one run of equivalent watches (seriesUnits) at a time: a run is
// removed when p still closes a cross-class cycle without it. The property
// is monotone (more watches never make p fit), so one pass leaves a minimal
// set, and since removing any single watch of a run has the same effect as
// removing the whole run, every watch left is needed on its own. One watch
// graph is reused, its edges toggled per trial.
func shrinkConflict(deps levelGraph, with []watchPair, p watchPair) []watchPair {
	lg := deps.withWatches(with)
	removed := map[watchPair]bool{}
	for _, unit := range seriesUnits(deps, with, p) {
		for _, k := range unit {
			lg.removeWatch(k)
		}
		if lg.closesCrossCycle(p) {
			for _, k := range unit {
				removed[k] = true
			}
			continue
		}
		for _, k := range unit {
			lg.addWatch(k)
		}
	}
	return slices.DeleteFunc(with, func(k watchPair) bool { return removed[k] })
}

// seriesUnits groups the watches of with into runs that are equivalent for
// shrinkConflict: maximal chains of watch edges whose inner ops are plain —
// no dependency in or out, not an end of p, and on exactly two watches of
// with. Such an op only passes a walk from one of its watches to the other,
// so cutting the run anywhere disconnects the same two ends. Runs follow the
// order of their first watch in with.
func seriesUnits(deps levelGraph, with []watchPair, p watchPair) [][]watchPair {
	at := map[int][]int{} // op → indexes into with
	for i, k := range with {
		at[k.gated] = append(at[k.gated], i)
		at[k.watched] = append(at[k.watched], i)
	}
	plain := func(op int) bool {
		return op != p.gated && op != p.watched && len(at[op]) == 2 &&
			len(deps.waiters[op]) == 0 && len(deps.incoming[op]) == 0
	}
	taken := make([]bool, len(with))
	var units [][]watchPair
	for i := range with {
		if taken[i] {
			continue
		}
		taken[i] = true
		unit := []watchPair{with[i]}
		for _, end := range []int{with[i].gated, with[i].watched} {
			for op := end; plain(op); {
				next := at[op][0]
				if taken[next] {
					next = at[op][1]
				}
				if taken[next] {
					break // the run closed on itself
				}
				taken[next] = true
				unit = append(unit, with[next])
				op = with[next].gated + with[next].watched - op
			}
		}
		units = append(units, unit)
	}
	return units
}

// conflictNote renders the kept watches a dropped one conflicts with for a
// refusal, as "the change watch A" or "the change watches A, B and C", at
// most three then a count of the rest.
func conflictNote(with []string) string {
	if len(with) == 1 {
		return "the change watch " + with[0]
	}
	if len(with) > 3 {
		with = append(with[:3:3], fmt.Sprintf("%d more", len(with)-3))
	}
	return "the change watches " + strings.Join(with[:len(with)-1], ", ") + " and " + with[len(with)-1]
}
