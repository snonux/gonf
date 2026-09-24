package api

import (
	"fmt"

	"github.com/snonux/gonf/internal/shellwords"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/noop"
	"github.com/snonux/gonf/resource/options"
)

// Sh registers a Command from one command line split into shell words,
// without running a shell: Sh("systemctl restart foo") is
// Command("systemctl", List("restart", "foo")) and records the identical
// plan op. Quotes and backslashes group and escape ('a b', "a b", a\ b);
// nothing is expanded, and an unquoted shell metacharacter (| & ; < > ( )
// $ ` * ? [, or # and ~ starting a word) or a $ or ` inside double quotes
// is a declaration error rather than a literal argument, since the line
// would not do what it reads like.
// Use Command with an explicit "sh", List("-c", ...) for real shell syntax.
//
// Without WithName the ID is Command's default, the split words joined by
// single spaces ("systemctl restart foo"), so quoting shows only in the
// argv, not in the ID: Sh(`echo 'a b'`) is Command[echo a b]. opts are
// every Command option (WithName, Unless, OnChange, WithSensitive, ...).
func Sh(command string, opts ...options.CommandOption) Resource {
	argv, err := shellwords.Split(command)
	if err != nil {
		// The command is not echoed: it may carry secret material.
		return resource.Refuse("Command", "Sh", fmt.Errorf("sh: %w", err))
	}
	return cmd.Present(argv[0], argv[1:], opts...)
}

// Noop registers a resource that changes nothing and always reports ok
// under Noop[name], e.g. a task that only verifies the push pipeline to a
// host. It replaces Command("true", nil, Unless("true", nil), WithName(name)),
// which ran two processes and reported skipped. A plan with a Noop needs a
// destination gonf with plan schema 25.
func Noop(name string) Resource {
	return noop.Present(name)
}
