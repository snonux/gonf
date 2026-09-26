// Command recipe is the first recipe of the gonf tutorial (chapter 2).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("hello", "Gonfy writes ~/hello.txt", func() {
		File(DestHome("hello.txt"), WithContent("Hello from Gonfy the beaver!\n"), WithMode(0o644))
	})
	cli.Main()
}
