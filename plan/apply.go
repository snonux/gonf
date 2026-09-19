package plan

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// Facts are live host values used to evaluate when_begin fact predicates.
type Facts struct {
	GOOS     string
	Profile  string
	Hostname string
}

// Apply interprets ops against live host facts and the local filesystem.
// ops[0] must be a plan header that passes ValidateHeader. Stackable
// when_begin/when_end blocks skip inactive bodies without mutation.
// Resource ops are topologically sorted by their deps within each contiguous
// run between control ops (plan header, when_begin, when_end), mirroring the
// repository path's dependency order; dep-free plans keep recorded order.
// Deps recorded in this body earlier, or applied by an earlier privilege
// chunk or invocation, count as satisfied; a dep recorded later in this body
// (later when-block) is refused before any mutation. A dep recorded nowhere
// in this body is satisfied at chunk level too — chunk boundaries are
// invisible to a chunk-level Apply; the controller-side pre-flight
// ValidateChunkDeps refuses forward cross-chunk and dangling deps before any
// chunk is applied.
// planDir is the directory containing blobs/ sidecars (usually next to the
// plan JSONL). Pass "" when the plan only uses content_b64 and no blobs.
// After applying (or refusing) the ops, the collected resource summary is
// printed to stderr — one summary per Apply invocation, mirroring the legacy
// repository path; chunked applies therefore print one summary per chunk.
func Apply(ops []Op, facts Facts, planDir string) error {
	if len(ops) == 0 {
		return fmt.Errorf("plan: apply: empty plan")
	}
	if err := ValidateHeader(ops[0]); err != nil {
		return err
	}

	resource.ResetReport()
	// The summary is what the report machinery exists for: every apply ends
	// with the collected outcomes, and a refused plan (sort errors) prints an
	// empty summary making clear nothing was applied. Deferred so partial
	// results are reported on error paths too.
	defer resource.PrintSummary(os.Stderr)

	// Sorting (and its dangling/cycle checks) runs before any mutation so a
	// refused plan leaves the destination untouched.
	body, err := sortedApplyOrder(ops[1:])
	if err != nil {
		return err
	}

	var stack []bool
	for _, l := range body {
		if err := applyLine(l.op, facts, planDir, &stack); err != nil {
			return fmt.Errorf("plan: apply line %d: %w", l.line, err)
		}
	}
	if len(stack) != 0 {
		return fmt.Errorf("plan: apply: %d unclosed when_begin", len(stack))
	}
	return nil
}

// planLine pairs an op with its original 1-based JSONL line number so
// apply-time errors name the recorded line even after dependency reordering.
type planLine struct {
	op   Op
	line int
}

// sortedApplyOrder returns the plan body in apply order: contiguous runs of
// resource ops between control ops (plan header, when_begin, when_end) are
// topologically sorted by their dep lists, mirroring the repository path.
// Control ops keep their recorded position, so resource ops are never
// reordered across when_* boundaries. A dep outside the current run is
// classified: recorded earlier in this body → satisfied (placed); recorded
// later in this body (first occurrence after the run) → refused, apply
// cannot reorder across the when_* boundary in between; recorded nowhere in
// this body → satisfied (an earlier privilege chunk or invocation applied
// it, and chunk boundaries are invisible to a chunk-level Apply). The
// controller-side ValidateChunkDeps pre-flight refuses forward cross-chunk
// and dangling deps before any chunk is applied.
func sortedApplyOrder(body []Op) ([]planLine, error) {
	// bodyIDs maps an op ID to its first recorded index in the body, so an
	// unmatched dep can be classified as later-in-body or absent entirely.
	bodyIDs := map[string]int{}
	for i, op := range body {
		if op.ID != "" {
			if _, seen := bodyIDs[op.ID]; !seen {
				bodyIDs[op.ID] = i
			}
		}
	}

	out := make([]planLine, 0, len(body))
	var run []planLine
	runEnd := 0 // body index just past the current run's last op
	// placed holds the op IDs of runs already emitted: their deps are
	// satisfied, because earlier segments and chunks always apply first.
	placed := map[string]bool{}
	flush := func() error {
		if len(run) == 0 {
			return nil
		}
		sorted, err := sortRunByDeps(run, placed, bodyIDs, runEnd)
		if err != nil {
			return err
		}
		out = append(out, sorted...)
		for _, l := range sorted {
			if l.op.ID != "" {
				placed[l.op.ID] = true
			}
		}
		run = run[:0]
		return nil
	}

	for i, op := range body {
		if IsControlKind(op.Op) {
			if err := flush(); err != nil {
				return nil, err
			}
			out = append(out, planLine{op: op, line: i + 2})
			continue
		}
		run = append(run, planLine{op: op, line: i + 2})
		runEnd = i + 1
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// sortRunByDeps topologically orders one contiguous resource-op run by the
// ops' dep lists (Kahn's algorithm, stable: among ready ops the earliest
// recorded one is emitted first). Deps are matched against op IDs. A dep on
// an op applied by an earlier run is satisfied. A dep whose first body
// occurrence is after this run (bodyIDs first index >= runEnd) is refused —
// apply cannot reorder it across the when_* boundary in between. A dep
// recorded nowhere in this body is satisfied: an earlier privilege chunk or
// invocation applied it, and chunk boundaries are invisible to a chunk-level
// Apply (ValidateChunkDeps refuses dangling deps controller-side before any
// chunk is applied).
func sortRunByDeps(run []planLine, placed map[string]bool, bodyIDs map[string]int, runEnd int) ([]planLine, error) {
	// inRun maps an op ID to every run position carrying it (IDs repeat when
	// a diamond include records the same resource twice).
	inRun := map[string][]int{}
	for pos, l := range run {
		if l.op.ID != "" {
			inRun[l.op.ID] = append(inRun[l.op.ID], pos)
		}
	}

	indeg := make([]int, len(run))
	waiters := make([][]int, len(run)) // dep position → dependent positions
	for pos, l := range run {
		for _, dep := range l.op.Deps {
			at, inCurrent := inRun[dep]
			if !inCurrent {
				if placed[dep] {
					continue // satisfied by an earlier run
				}
				if first, inBody := bodyIDs[dep]; inBody && first >= runEnd {
					return nil, fmt.Errorf(
						"plan: op %s depends on %s which is not ordered before it; later when-block dependencies cannot be reordered before it",
						l.op.ID, dep)
				}
				// Recorded nowhere in this body: satisfied by an earlier
				// privilege chunk or invocation (chunks never reorder).
				continue
			}
			for _, p := range at {
				indeg[pos]++
				waiters[p] = append(waiters[p], pos)
			}
		}
	}
	return kahnStable(run, indeg, waiters)
}

// kahnStable emits the run's ops in dependency order, breaking ties by
// recorded position: among the currently ready ops, the earliest recorded one
// always goes first. A leftover indegree at the end means the run has a
// dependency cycle, mirroring the repository path's cycle error.
func kahnStable(run []planLine, indeg []int, waiters [][]int) ([]planLine, error) {
	ready := make([]int, 0, len(run))
	for pos, n := range indeg {
		if n == 0 {
			ready = append(ready, pos) // ascending: pos is appended in order
		}
	}

	sorted := make([]planLine, 0, len(run))
	for len(ready) > 0 {
		pos := ready[0]
		ready = ready[1:]
		sorted = append(sorted, run[pos])
		for _, w := range waiters[pos] {
			indeg[w]--
			if indeg[w] == 0 {
				// Keep ready ascending by recorded position (stable Kahn).
				at := sort.SearchInts(ready, w)
				ready = append(ready, 0)
				copy(ready[at+1:], ready[at:])
				ready[at] = w
			}
		}
	}
	for pos, n := range indeg {
		if n > 0 {
			return nil, fmt.Errorf("plan: circular dependency involving %s", run[pos].op.ID)
		}
	}
	return sorted, nil
}

func applyLine(op Op, facts Facts, planDir string, stack *[]bool) error {
	active := whenActive(*stack)

	switch op.Op {
	case KindWhenBegin:
		ok := false
		if active {
			var err error
			ok, err = evalAll(op.All, facts)
			if err != nil {
				return err
			}
		}
		*stack = append(*stack, active && ok)
		return nil

	case KindWhenEnd:
		if len(*stack) == 0 {
			return fmt.Errorf("when_end without matching when_begin")
		}
		*stack = (*stack)[:len(*stack)-1]
		return nil

	case KindPlan:
		return fmt.Errorf("duplicate plan header")
	}

	if !active {
		return nil
	}
	return applyActive(op, planDir)
}

func whenActive(stack []bool) bool {
	if len(stack) == 0 {
		return true
	}
	return stack[len(stack)-1]
}

// applyActive dispatches an active (non-control) op to its resource kind's
// registered plan.Handler (see handler.go). Every resource kind registers
// one from its own package's init() — see docs/plan.md, "Adding a resource
// kind" — so this package needs no resource-package imports of its own: it
// only knows the Handler interface, never a concrete resource kind.
func applyActive(op Op, planDir string) error {
	if h, ok := HandlerFor(op.Op); ok {
		return h.Apply(op, ApplyContext{PlanDir: planDir})
	}
	return fmt.Errorf("unknown op %q", op.Op)
}

// OwnerGroupOptions converts an op's recorded owner/group into options. Both
// are only appended when non-empty: an omitted field must leave ownership to
// the apply-side defaults instead of forcing WithOwner(""). Shared by every
// resource kind's plan.Handler.Apply that carries owner/group on the wire
// (file, dir, sync_dir, ensure_dir), so the "empty means unset" rule cannot
// drift between them.
func OwnerGroupOptions(op Op) []opt.FileDirOption {
	var opts []opt.FileDirOption
	if op.Owner != "" {
		opts = append(opts, opt.WithOwner(op.Owner))
	}
	if op.Group != "" {
		opts = append(opts, opt.WithGroup(op.Group))
	}
	return opts
}

// GuardOptions converts a recorded Guard into the single Unless or OnlyIf
// option a command resource expects, selected by unless. It is the plan-wire
// counterpart of resource/cmd's own guard option builders, exported so
// resource/cmd's plan.Handler.Apply (the only current caller) does not
// duplicate the field mapping.
func GuardOptions(g *Guard, unless bool) []opt.CommandOption {
	var gopts []opt.GuardOption
	if g.ExpectStdout != "" {
		gopts = append(gopts, opt.ExpectStdout(g.ExpectStdout))
	}
	if g.ExpectExit != nil {
		gopts = append(gopts, opt.ExpectExit(*g.ExpectExit))
	}
	if unless {
		return []opt.CommandOption{opt.Unless(g.Bin, append([]string(nil), g.Args...), gopts...)}
	}
	return []opt.CommandOption{opt.OnlyIf(g.Bin, append([]string(nil), g.Args...), gopts...)}
}

func evalAll(preds []Predicate, facts Facts) (bool, error) {
	for _, p := range preds {
		ok, err := evalPredicate(p, facts)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func evalPredicate(p Predicate, facts Facts) (bool, error) {
	switch {
	case p.PathExists != "":
		path, err := ExpandPath(p.PathExists)
		if err != nil {
			return false, err
		}
		return pathExists(path)
	case p.Fact != "":
		return evalFact(p.Fact, p.Eq, p.In, facts)
	default:
		return false, fmt.Errorf("empty predicate")
	}
}

func evalFact(name, eq string, in []string, facts Facts) (bool, error) {
	switch name {
	case "goos":
		return matchesEqOrIn(facts.GOOS, eq, in), nil
	case "profile":
		return matchesEqOrIn(facts.Profile, eq, in), nil
	case "hostname_contains":
		return matchesContainsOrIn(facts.Hostname, eq, in), nil
	default:
		return false, fmt.Errorf("unknown fact %q", name)
	}
}

// matchesEqOrIn reports whether value equals eq, or — when in is non-empty —
// equals any entry of in. In takes precedence over Eq (WhenProfile's
// multi-profile OR lowers to In only, leaving Eq empty).
func matchesEqOrIn(value, eq string, in []string) bool {
	if len(in) > 0 {
		for _, want := range in {
			if value == want {
				return true
			}
		}
		return false
	}
	return value == eq
}

// matchesContainsOrIn reports whether hostname contains eq (case
// insensitive), or — when in is non-empty — contains any entry of in.
func matchesContainsOrIn(hostname, eq string, in []string) bool {
	host := strings.ToLower(hostname)
	if len(in) > 0 {
		for _, want := range in {
			if strings.Contains(host, strings.ToLower(want)) {
				return true
			}
		}
		return false
	}
	return strings.Contains(host, strings.ToLower(eq))
}

func pathExists(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("path_exists: empty path")
	}
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
