package main

import (
	"os"

	//lint:ignore ST1001 intentional: gonf's task DSL (Command, File, Dir,
	// ...) is designed to be called unqualified, exactly as client repos
	// (e.g. conf/gonf, dotfiles/gonf) do in their own main.go.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/examples"
	"github.com/snonux/gonf/internal/cli"
)

func main() {
	RegisterMethods(examples.Demo{}, WithPrefix("demo_"))
	Aggregate("demo", "Run all demo_* tasks", "^demo_")
	os.Exit(cli.CLI())
}
