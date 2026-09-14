package api

import "log"

// Aggregate registers a task that runs every activated task whose name matches
// pattern (via Matching). A Run error is fatal.
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
			log.Fatalf("aggregate %s: pattern %q matched no tasks (after excluding itself)", name, pattern)
		}
		if err := Run(names...); err != nil {
			log.Fatal(err)
		}
	})
}
