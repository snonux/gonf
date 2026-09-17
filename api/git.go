package api

import (
	//lint:ignore ST1001 intentional: this file uses the fluent options DSL
	// unqualified, matching how client tasks (and cmd/gonf/main.go,
	// examples/examples.go) consume the gonf API.
	. "github.com/snonux/gonf/api/options"
)

// GitGlobal sets each key/value via `git config --global`, skipping when the
// current value already matches (Unless + ExpectStdout).
func GitGlobal(kv ...string) {
	EachKV(kv, func(key, val string) {
		Command("git", List("config", "--global", key, val),
			Unless("git", List("config", "--global", "--get", key), ExpectStdout(val)),
			WithName("git."+key),
		)
	})
}
