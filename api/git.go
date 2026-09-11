package api

import (
	. "github.com/snonux/gonf/api/options"
)

// GitGlobal sets each key/value via `git config --global`, skipping when the
// current value already matches (Unless + ExpectStdout).
func GitGlobal(kv ...string) {
	EachKV(kv, func(key, val string) {
		Command("git", Elems("config", "--global", key, val),
			Unless("git", Elems("config", "--global", "--get", key), ExpectStdout(val)),
			WithName("git."+key),
		)
	})
}
