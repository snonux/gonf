// Command gonf shows gonf's error classes (tutorial chapter 15). Set
// BREAK to one of decl, record or apply to trigger one of them.
package main

import (
	"os"

	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	brk := os.Getenv("BREAK")
	dir := DestHome("gonf-tutorial/trouble")

	if brk == "decl" {
		// Declaration error: line edits cannot be combined with content.
		File("/tmp/broken.conf", WithContent("a=1\n"), WithLines("b=2"))
	}

	Task("setup", "Create the working directory", func() { Dir(dir, WithMode(0o755)) })
	Task("app", "Write the app config", func() {
		File(dir+"/app.conf", WithContent("ok\n"), WithMode(0o644))
		if brk == "apply" {
			// Apply error: the command fails on the destination.
			Command("false", nil, WithName("always-fails"))
		}
	}, Needs(needed(brk)))
	cli.Main()
}

// needed names the task app needs; BREAK=record names one that does not
// exist, which fails when the plan is recorded.
func needed(brk string) string {
	if brk == "record" {
		return "setpu"
	}
	return "setup"
}
