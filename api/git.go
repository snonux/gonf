package api

import (
	"strings"

	"github.com/snonux/gonf/resource/options"
)

// GitGlobal sets each key/value via `git config --global`, skipping when the
// current value already matches (Unless + ExpectStdout). ExpectStdout
// compares trimmed output and treats "" as no expectation, so a value that
// is empty or has surrounding whitespace cannot be checked that way: it is
// set on every apply instead of never (empty) or reported changed forever
// with a guard that can never match.
func GitGlobal(kv ...string) {
	EachKV(kv, func(key, val string) {
		opts := []options.CommandOption{WithName("git." + key)}
		if val != "" && val == strings.TrimSpace(val) {
			opts = append(opts, Unless("git", List("config", "--global", "--get", key), ExpectStdout(val)))
		}
		Command("git", List("config", "--global", key, val), opts...)
	})
}
