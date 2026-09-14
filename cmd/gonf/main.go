package main

import (
	"os"

	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/examples"
	"github.com/snonux/gonf/internal/cli"
)

func main() {
	RegisterMethods(examples.Demo{}, WithPrefix("demo_"))
	Aggregate("demo", "Run all demo_* tasks", "^demo_")
	os.Exit(cli.CLI())
}
