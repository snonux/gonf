# 17. gonf from Go

Your gonf ends with `cli.Main()`, which turns the program into the gonf
command with all its subcommands. Sometimes you want a program of your own
instead: a deploy tool with its own flags, a test that converges a
scratch directory, or a service that pushes on a schedule. The `api`
package has the functions the CLI itself uses, so such a program is
ordinary Go.

> 🦫 **Gonfy says:** The CLI is the tool I carry every day. The library is the workshop it was built in, open for when you need a tool of your own.

## The program

`lodgectl` registers one task, looks at its plan and then applies it here,
or pushes it when you name a host:

```go
// Command lodgectl drives gonf from Go instead of cli.Main (tutorial
// chapter 17).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
```

- There is no `cli.Main()`: the program decides what to do, and none of the
  gonf flags or subcommands exist.
- `RecordPlanTo` records the tasks into a blob store you pass, here one in
  memory, and returns the ops. Nothing is applied.
- `RunContext` records and applies on this machine, like `./gonf lodge`.
- `PushToContext` pushes to one ssh destination, like `gonf push`. It
  installs gonf on the host when needed, as `push` does.
- Without `cli.Main()`, Ctrl-C is yours to handle. `signal.NotifyContext`
  gives a context that a signal cancels, and gonf then stops as described
  in chapter 15.

## Run it

Build it in `docs/tutorial/examples`, run it twice here and once against
`earth` from chapter 12:

```text
$ go build -o lodgectl ./ch17-library
$ ./lodgectl
recorded plan lodgectl
recorded dir Directory[${HOME}/gonf-tutorial/library]
recorded file File[${HOME}/gonf-tutorial/library/lodge]
2026/09/30 04:20:04 created directory /home/paul/gonf-tutorial/library
2026/09/30 04:20:04 updated /home/paul/gonf-tutorial/library/lodge
summary: 0 ok, 2 changed, 0 skipped, 0 would-change
$ ./lodgectl
recorded plan lodgectl
recorded dir Directory[${HOME}/gonf-tutorial/library]
recorded file File[${HOME}/gonf-tutorial/library/lodge]
summary: 2 ok, 0 changed, 0 skipped, 0 would-change
$ ./lodgectl paul@earth.lan
recorded plan lodgectl
recorded dir Directory[${HOME}/gonf-tutorial/library]
recorded file File[${HOME}/gonf-tutorial/library/lodge]
2026/09/30 04:20:04 created directory /root/gonf-tutorial/library
2026/09/30 04:20:04 updated /root/gonf-tutorial/library/lodge
summary: 0 ok, 2 changed, 0 skipped, 0 would-change
applied stdin (3 ops)
pushed lodgectl (3 ops) to paul@earth.lan
```

The log lines and summaries are the same as the CLI's.

## The API

| Function | What it does |
|----------|--------------|
| `Run(tasks...)`, `RunContext(ctx, tasks...)` | record and apply here, like `gonf <task>` |
| `RecordPlan(id, dir, tasks...)` | record, and write the blobs into `dir` with the rules of `gonf plan -o dir`; you write the ops out yourself |
| `RecordPlanTo(id, store, tasks...)` | record into a blob store you own, such as `plan.NewMemoryStore()` |
| `ApplyChunks(ops, dir, mode)` | apply recorded ops, split by privilege |
| `Apply()` | apply resources declared outside any task |
| `PushTo(target, id, tasks...)`, `PushToContext` | push to a `PushTarget`, like `gonf push` |
| `PushHost(MustHost("earth"), tasks...)` | push to an inventory host with its own settings |
| `PushCluster`, `PushFleet`, `PushClusterRun`, `PushFleetRun` | like `gonf cluster` and `gonf fleet`; the `Run` forms take a context, `-j` and `-host-timeout` |
| `PreviewTo`, `PreviewHost` | like `push -preview` |
| `SetCommandTimeout`, `SetProfileOverride`, `SetPrivilege` | the `-cmd-timeout`, `-profile` and `-privilege` flags |
| `Tasks()`, `Matching(regex)` | the task list behind `-list` |
| `cli.CLI()` | `cli.Main()` without exiting: it returns the exit code |

The [reference](../reference.md#go-api) lists the rest, such as
`RecordPlanForHost` for one host's `ForHosts` selection,
`RecordPlanDeferred` for sealing after recording, and `PushPayload` for a
plan you encoded yourself.

A few rules:

- Record one plan at a time. Recording is not safe from several goroutines.
- Privileged tasks (chapter 10) need `cli.Main()` or `cli.CLI()` for a
  local run: gonf re-runs your program through sudo or doas as `apply`,
  and only the CLI understands that. Run privileged work as root, or push
  it with `PushTarget{Privilege: PrivilegeSudo}`.
- A declaration error (chapter 15) sticks: every later record or apply
  refuses with it. `resource.ResetDeclarationError()` returns and clears
  it. After a failed secret lookup, also call `resource.ResetRepository()`
  and declare everything again, because a resource may already hold an
  empty value.
- Library functions return errors; they never exit the process.

## Every subcommand

For reference, here is every subcommand your gonf has, and where this
book covers it:

| Subcommand | Chapter |
|------------|---------|
| `gonf <task>...`, `-list`, `-n` | 2 |
| `plan`, `apply` | 11 |
| `push`, `cluster`, `fleet`, `hosts`, `clusters`, `fleets` | 12 |
| `plan-signer-keygen`, `plan-verify` | 14 |
| `dns-zone-serial`, `dns-zone-equivalent` | below |
| `-version`, `-plan-version`, `-strict-preview-version`, `-sealed-version`, `-signed-version` | 11, 12, 14, 15 |

The two DNS helpers are for scripts that publish DNS zone files.
`gonf dns-zone-serial <origin> <zone>` prints the serial number of the
zone's SOA record. `gonf dns-zone-equivalent <origin> <new> <old>` exits 0
when both zones hold the same records, ignoring the serial and the record
order, and 1 when they differ, so a script bumps the serial only for a
real change.

Reference: [Go API](../reference.md#go-api),
[Error handling](../reference.md#error-handling),
[Subcommands](../reference.md#subcommands).

---

← [16. A web fleet](16-web-fleet.md) · [Contents](README.md)
