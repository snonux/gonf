package api

import "github.com/snonux/gonf/plan"

// orderForPrivilegeSplit returns the ops of an api.Apply plan in an order
// that plan.SplitPrivilegeChunks can cut into privilege chunks without a
// dependency pointing into a later chunk.
//
// Apply's op order carries no meaning of its own: RegisteredPlanDrafts
// returns the drafts sorted by resource ID, and within one chunk plan.Apply
// reorders by deps anyway. Split in that ID order, an unprivileged File that
// an elevated Command depends on could land in a later chunk merely because
// "Command[" sorts before "File[", and the pre-flight would refuse a
// perfectly valid recipe. So before splitting, the body is put into
// dependency order (Kahn's algorithm) that also keeps ops of one privilege
// class together while it can (sameClassKahn), which yields the fewest
// elevation round trips the dependency graph allows.
//
// ops[0] is the plan header and stays first. Apply lowers registered
// resources to a flat op list (no when_begin/when_end), so the whole body is
// one sortable run. Deps naming no op of the plan are ignored here; the
// pre-flight refuses them as dangling. On a dependency cycle ops is returned
// unchanged so the plan engine reports the cycle in its usual words.
func orderForPrivilegeSplit(ops []plan.Op) []plan.Op {
	if len(ops) < 2 {
		return ops
	}
	body := ops[1:]
	order, ok := sameClassKahn(body)
	if !ok {
		return ops
	}
	out := make([]plan.Op, 0, len(ops))
	out = append(out, ops[0])
	for _, i := range order {
		out = append(out, body[i])
	}
	return out
}

// sameClassKahn topologically orders body by its ops' deps and returns the
// body indexes in that order, or ok=false on a cycle. Among the ready ops it
// prefers the lowest-indexed one of the privilege class (Elevate) emitted
// last, and only switches class when no op of that class is ready; the first
// op picked is the lowest-indexed ready one. Ties therefore keep the incoming
// (resource ID) order, so the result is deterministic.
func sameClassKahn(body []plan.Op) (order []int, ok bool) {
	indeg, waiters := depGraph(body)
	emitted := make([]bool, len(body))
	order = make([]int, 0, len(body))
	class, started := false, false
	for len(order) < len(body) {
		next := pickReady(body, indeg, emitted, class, started)
		if next < 0 {
			return nil, false // every remaining op waits on another: a cycle
		}
		emitted[next] = true
		class, started = body[next].Elevate, true
		order = append(order, next)
		for _, w := range waiters[next] {
			indeg[w]--
		}
	}
	return order, true
}

// depGraph returns each op's count of unmet in-plan deps and, per op, the
// ops waiting on it. A dep matching no op ID in body adds no edge.
func depGraph(body []plan.Op) (indeg []int, waiters [][]int) {
	byID := make(map[string]int, len(body))
	for i, op := range body {
		if op.ID != "" {
			byID[op.ID] = i
		}
	}
	indeg = make([]int, len(body))
	waiters = make([][]int, len(body))
	for i, op := range body {
		for _, dep := range op.Deps {
			if at, found := byID[dep]; found && at != i {
				indeg[i]++
				waiters[at] = append(waiters[at], i)
			}
		}
	}
	return indeg, waiters
}

// pickReady returns the lowest-indexed ready op (no unmet deps, not yet
// emitted) of class, falling back to the lowest-indexed ready op of any class
// when none of class is ready or nothing has been emitted yet (started is
// false). It returns -1 when no op is ready.
func pickReady(body []plan.Op, indeg []int, emitted []bool, class, started bool) int {
	fallback := -1
	for i := range body {
		if emitted[i] || indeg[i] > 0 {
			continue
		}
		if !started || body[i].Elevate == class {
			return i
		}
		if fallback < 0 {
			fallback = i
		}
	}
	return fallback
}
