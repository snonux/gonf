// Command lodgectl drives gonf from Go instead of cli.Main (tutorial
// chapter 17).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/plan"
)

func main() {
	Task("lodge", "Write Gonfy's lodge note", func() {
		dir := DestHome("gonf-tutorial/library")
		Dir(dir, WithMode(0o755))
		File(dir+"/lodge", WithContent("Built by lodgectl.\n"), WithMode(0o644))
	})

	// Ctrl-C and SIGTERM cancel ctx, and gonf stops cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Record into memory to look at the plan without applying it.
	ops, err := RecordPlanTo("lodgectl", plan.NewMemoryStore(), "lodge")
	if err != nil {
		fail(err)
	}
	for _, op := range ops {
		fmt.Println("recorded", op.Op, op.ID)
	}

	if len(os.Args) > 1 {
		// lodgectl user@host: record again and push over ssh.
		err = PushToContext(ctx, PushTarget{Host: os.Args[1]}, "lodgectl", "lodge")
	} else {
		// No argument: record and apply here, like ./gonf lodge.
		err = RunContext(ctx, "lodge")
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "lodgectl:", err)
	os.Exit(1)
}
