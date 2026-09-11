package main

import (
	"os"

	"github.com/snonux/gonf/api"
	"github.com/snonux/gonf/examples"
)

func main() {
	examples.Register()
	os.Exit(api.CLI())
}
