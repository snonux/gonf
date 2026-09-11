package api

import "log"

// Aggregate registers a task that runs every activated task whose name matches
// pattern (via Matching). A Run error is fatal.
func Aggregate(name, description, pattern string) {
	Task(name, description, func() {
		if err := Run(Matching(pattern)...); err != nil {
			log.Fatal(err)
		}
	})
}
