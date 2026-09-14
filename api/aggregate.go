package api

import "fmt"

// Aggregate registers a task that runs every activated task whose name matches
// pattern (via Matching).
//
// Error handling: a Task fn cannot return errors, so a failure inside the
// body — the pattern matching no tasks, or a child task failing to record —
// is stashed (stashBodyError) and fails the surrounding RecordPlan session
// with a returned error naming the aggregate and the underlying child error.
// Nothing is applied in that case: the abort happens during recording, before
// plan apply runs. This mirrors the plan recorder's cycle-stash mechanism and
// keeps Run's deferred temp-dir cleanup intact (no process exit).
func Aggregate(name, description, pattern string) {
	Task(name, description, func() {
		// Exclude the aggregate's own name: a pattern that matches it (e.g.
		// ".*") would otherwise make the aggregate recurse into itself. The
		// plan recorder's cycle detector still catches deeper recursion.
		var names []string
		for _, n := range Matching(pattern) {
			if n != name {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			stashBodyError(fmt.Errorf(
				"aggregate %s: pattern %q matched no tasks (after excluding itself)",
				name, pattern))
			return
		}
		if err := Run(names...); err != nil {
			stashBodyError(fmt.Errorf("aggregate %s: %w", name, err))
		}
	})
}
