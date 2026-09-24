package plan

import (
	"path"
	"slices"
)

// Parent-directory dependency inference.
//
// A resource whose destination path lies inside a directory another op of
// the same plan creates must apply after that directory, even when the
// recipe wrote no DependsOn for it: File("/usr/local/sbin/x") next to
// EnsureDir("/usr/local/sbin") must not run first and fail on a missing
// parent. Such an edge orders exactly like a recorded DependsOn of the
// consumer on the directory: the stable sort delays the consumer until the
// directory applied, and everything else keeps its relative order. So, as
// with an explicit DependsOn, an op declared between a consumer and its
// later-declared directory no longer follows the consumer unless it depends
// on it (OnChange included); a WatchChanges gate there, which orders
// nothing, may then run before the change it watches. The edges are derived
// from the recorded paths when the apply order is computed (plan.Apply's
// per-run sort, and api.Apply's privilege split), never recorded into
// Op.Deps: a plan is byte-identical with or without them, and an explicit
// DependsOn next to an inferred edge is simply a duplicate edge.
//
// The rules, all lexical (path.Clean of the recorded path, no filesystem
// access, so a symlink in either path is not resolved):
//
//   - A provider is a present (not Absent) dir, sync_dir or ensure_dir op.
//     Links are never providers, even when they point at a directory.
//   - A consumer is a present op that creates something at a path: file,
//     ensure_file, dir, sync_dir, ensure_dir, link, link_if_exists, and a
//     config_set through each of its members' paths. An Absent op creates
//     nothing and gets no inferred edge; a present op inside an Absent
//     directory is a recipe conflict that inference neither orders nor
//     refuses (the declared order stands, as without inference).
//   - A consumer depends on the providers at its NEAREST strict ancestor
//     path only (all providers at that one path, if several declare it).
//     Nested directories chain: /a/b/c after /a/b, /a/b after /a.
//   - An inferred edge is only a hint: it is dropped when it would close a
//     cycle with the caller's existing edges or with inferred edges accepted
//     before it (consumers in op order). It therefore never turns an
//     acceptable plan into a refused one: no new cycle, and nothing the
//     ValidateChunks pre-flight checks (which only reads Op.Deps).
//
// Scope is the caller's: plan.Apply infers within one contiguous run between
// control ops (so never across when_begin/when_end, and never across
// privilege chunks, which apply as separate plan.Apply calls); api.Apply
// infers over its whole flat body before choosing the chunk order.

// ParentDirEdge is one inferred edge, by index into the ops passed to
// InferParentDirDeps: Dependent applies after Dep.
type ParentDirEdge struct{ Dep, Dependent int }

// InferParentDirDeps returns the parent-directory edges of ops (see the
// rules above), in consumer order. next enumerates the caller's existing
// edges leaving op u (u must apply before every v it visits); it may be nil
// for a graph without edges. An edge is accepted only if its dependent does
// not already reach its dep through next plus the edges accepted before it,
// so adding every returned edge to the caller's graph keeps it acyclic
// wherever it was acyclic. Cost: O(n * depth) to find the candidates, plus
// one O(n + E) search per candidate edge.
func InferParentDirDeps(ops []Op, next func(u int, visit func(v int))) []ParentDirEdge {
	providers := dirProviders(ops)
	if len(providers) == 0 {
		return nil
	}
	r := newReacher(len(ops), next)
	var edges []ParentDirEdge
	for i, op := range ops {
		for _, dep := range nearestProviders(op, i, providers) {
			if r.reaches(i, dep) {
				continue // dep already follows i: the edge would close a cycle
			}
			r.extra[dep] = append(r.extra[dep], i)
			edges = append(edges, ParentDirEdge{Dep: dep, Dependent: i})
		}
	}
	return edges
}

// dirProviders maps each cleaned directory path some present op creates to
// the indexes of those ops.
func dirProviders(ops []Op) map[string][]int {
	var providers map[string][]int
	for i, op := range ops {
		if !createsDir(op) {
			continue
		}
		if providers == nil {
			providers = map[string][]int{}
		}
		p := path.Clean(op.Path)
		providers[p] = append(providers[p], i)
	}
	return providers
}

// createsDir reports whether op is a directory provider.
func createsDir(op Op) bool {
	switch op.Op {
	case KindDir, KindSyncDir, KindEnsureDir:
		return !op.Absent && op.Path != ""
	default:
		return false
	}
}

// createdPaths returns the destination paths a present consumer op creates
// something at, or nil for an op that is no consumer.
func createdPaths(op Op) []string {
	if op.Absent {
		return nil
	}
	switch op.Op {
	case KindFile, KindEnsureFile, KindDir, KindSyncDir, KindEnsureDir, KindLink, KindLinkIfExists:
		if op.Path == "" {
			return nil
		}
		return []string{op.Path}
	case KindConfigSet:
		members := PayloadOf[ConfigSetPayload](op).Members
		paths := make([]string, 0, len(members))
		for _, m := range members {
			if m.Path != "" {
				paths = append(paths, m.Path)
			}
		}
		return paths
	default:
		return nil
	}
}

// nearestProviders returns, without duplicates, the providers at the nearest
// strict ancestor of each path op (at index self) creates, never self.
func nearestProviders(op Op, self int, providers map[string][]int) []int {
	var deps []int
	for _, p := range createdPaths(op) {
		for _, dep := range providers[nearestAncestor(path.Clean(p), providers)] {
			if dep != self && !slices.Contains(deps, dep) {
				deps = append(deps, dep)
			}
		}
	}
	return deps
}

// nearestAncestor returns the longest strict ancestor of the cleaned path p
// that providers holds, or "" when none does. It walks up lexically
// (path.Dir) until the walk stops moving ("/" or ".").
func nearestAncestor(p string, providers map[string][]int) string {
	for {
		parent := path.Dir(p)
		if parent == p {
			return ""
		}
		if _, ok := providers[parent]; ok {
			return parent
		}
		p = parent
	}
}

// reacher answers reachability over the caller's edges (next) plus the
// inferred edges accepted so far (extra). seen is stamp-marked scratch space
// (bumping stamp clears it) and queue is reused between searches.
type reacher struct {
	next  func(u int, visit func(v int))
	extra [][]int
	seen  []int
	stamp int
	queue []int
}

func newReacher(n int, next func(u int, visit func(v int))) *reacher {
	return &reacher{next: next, extra: make([][]int, n), seen: make([]int, n)}
}

// reaches reports whether to is reachable from from (breadth-first).
func (r *reacher) reaches(from, to int) bool {
	r.stamp++
	r.queue = append(r.queue[:0], from)
	r.seen[from] = r.stamp
	found := false
	visit := func(v int) {
		if found || r.seen[v] == r.stamp {
			return
		}
		r.seen[v] = r.stamp
		found = v == to
		r.queue = append(r.queue, v)
	}
	for k := 0; k < len(r.queue) && !found; k++ {
		u := r.queue[k]
		if r.next != nil {
			r.next(u, visit)
		}
		for _, v := range r.extra[u] {
			visit(v)
		}
	}
	return found || from == to
}
