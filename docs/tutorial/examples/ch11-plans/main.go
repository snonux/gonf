// Command recipe records plans to apply later or elsewhere (tutorial
// chapter 11).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("dotfiles", "Shell and vim dotfiles", func() {
		base := DestHome("gonf-tutorial/plans")
		Dir(base, WithMode(0o755))
		File(base+"/bashrc", WithSource("assets/dotfiles/bashrc"), WithMode(0o644))
		Dir(base+"/vim", WithSource("assets/dotfiles/vim"), WithPrune, WithFileMode(0o644))
	}, WhenLinux())
	Task("greeting", "A small plan without blobs", func() {
		File(DestHome("gonf-tutorial/greeting"), WithContent("hi from gonfy\n"), WithMode(0o644))
	})
	cli.Main()
}
