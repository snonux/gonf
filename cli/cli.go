// Package cli exposes gonf's command-line interface for embedding: configs
// that are Go programs (like the dotfiles module) call cli.CLI from their own
// main to get flag parsing, subcommand dispatch and the standard exit-code
// handling without re-implementing it.
package cli

import (
	"os"

	internalcli "github.com/snonux/gonf/internal/cli"
)

// CLI parses os.Args, dispatches the gonf subcommands and returns the
// process exit code (0 on success). The DSL and task registry live in the
// api package; this entry point ties them to the command line.
func CLI() int {
	return internalcli.CLI()
}

// Main runs CLI and exits the process with its exit code: a recipe's main
// ends with cli.Main() instead of os.Exit(cli.CLI()).
func Main() {
	os.Exit(CLI())
}
