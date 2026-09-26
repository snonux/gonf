# 2. Your first recipe

A recipe is a normal Go module. In this chapter you create one, add a task
in which Gonfy writes a file into your home directory, and run it a few times to see
how gonf reports changes.

> 🦫 **Gonfy says:** Run it, then run it again. When the second run says `0 changed`, the lodge is sound.

## Create the module

> 🦫 **Gonfy says:** A recipe is just a Go module. No special tools, only Go.

```text
$ mkdir myconf
$ cd myconf
$ go mod init example.com/myconf
go: creating new go.mod: module example.com/myconf
```

Write `main.go` (it is also in
[examples/ch02-first-recipe](examples/ch02-first-recipe/main.go)):

```go
package main

import (
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("hello", "Gonfy writes ~/hello.txt", func() {
		File(DestHome("hello.txt"), WithContent("Hello from Gonfy the beaver!\n"), WithMode(0o644))
	})
	cli.Main()
}
```

Line by line:

- `. "github.com/snonux/gonf/api"` is a dot import. The whole DSL (`Task`,
  `File`, `WithContent`, ...) is meant to be used unqualified, and this one
  import is all a recipe needs.
- `Task(name, description, body)` registers a task. Nothing runs yet.
- `File(path, options...)` declares a file resource inside the task body.
- `DestHome("hello.txt")` is `~/hello.txt` on the machine that applies the
  plan. Chapter 3 explains why this is not `os.Getenv("HOME")`.
- `cli.Main()` hands over to gonf's command line, which parses flags, runs
  the tasks you name and exits with a status code.

Fetch gonf and build. This book follows gonf's main branch, so use `@main`
until the next release is tagged (then `@latest` works too):

```text
$ go get github.com/snonux/gonf@main
go: added github.com/snonux/gonf v0.23.1-0.20260926080836-0c49fa3f3fef
$ go mod tidy
$ go build -o gonf .
```

The result is your own `gonf`: gonf's full command line with your tasks
compiled in. There is no separate gonf program that reads recipes; every
recipe repository builds its own gonf like this one. The tutorial runs it
as `./gonf`.

## List, preview, apply

`-list` prints every task with its description:

```text
$ ./gonf -list
hello	Gonfy writes ~/hello.txt
```

`-n` (or `-dry-run`) shows what would change, without changing anything:

```text
$ ./gonf -n hello
2026/09/26 08:25:12 dry-run: would update /home/paul/hello.txt
summary: 0 ok, 0 changed, 0 skipped, 1 would-change
  would-change File[/home/paul/hello.txt]
```

Every resource has an **ID** such as `File[/home/paul/hello.txt]`. The
summary lists the IDs of everything that is not `ok`.

Now apply it by naming the task:

```text
$ ./gonf hello
2026/09/26 08:25:12 updated /home/paul/hello.txt
summary: 0 ok, 1 changed, 0 skipped, 0 would-change
  changed File[/home/paul/hello.txt]
$ cat ~/hello.txt
Hello from Gonfy the beaver!
```

## Run it again

```text
$ ./gonf hello
summary: 1 ok, 0 changed, 0 skipped, 0 would-change
```

Nothing changed, because the file already matches. Now let something
nibble on the file behind gonf's back and look at the drift:

```text
$ echo nibbled > ~/hello.txt
$ ./gonf -n hello
2026/09/26 08:25:12 dry-run: would update /home/paul/hello.txt
summary: 0 ok, 0 changed, 0 skipped, 1 would-change
  would-change File[/home/paul/hello.txt]
$ ./gonf hello
2026/09/26 08:25:12 updated /home/paul/hello.txt
summary: 0 ok, 1 changed, 0 skipped, 0 would-change
  changed File[/home/paul/hello.txt]
```

This is the core loop of gonf: declare, preview with `-n`, apply, and run
again whenever you like. Gonfy calls it his evening walk around the lodge.

## Change the recipe, then rebuild

Your tasks are compiled into your gonf, so editing `main.go` changes
nothing until you build again. Change the greeting in `main.go` to
`"Hello from Gonfy and the whole lodge!\n"` and run the old binary:

```text
$ ./gonf -n hello
summary: 1 ok, 0 changed, 0 skipped, 0 would-change
```

The old binary still carries the old greeting, so it sees nothing to do.
Rebuild, and the change shows up:

```text
$ go build -o gonf .
$ ./gonf -n hello
2026/09/26 08:25:13 dry-run: would update /home/paul/hello.txt
summary: 0 ok, 0 changed, 0 skipped, 1 would-change
  would-change File[/home/paul/hello.txt]
$ ./gonf hello
2026/09/26 08:25:13 updated /home/paul/hello.txt
summary: 0 ok, 1 changed, 0 skipped, 0 would-change
  changed File[/home/paul/hello.txt]
```

So the full loop after every edit is: rebuild, preview with `-n`, apply.
While you iterate, `go run . -n hello` builds and runs in one step. The
rest of this book builds once per chapter; rebuild whenever you change a
recipe.

You rebuild only on the controller, the machine where you run your gonf.
Remote hosts (chapter 12) never see your recipe: your gonf records a plan
and ships that, and gonf keeps the binary on each host up to date by
itself.

## A few more flags

> 🦫 **Gonfy says:** `-quiet` is for when you trust me and only want the summary.

```text
$ ./gonf -quiet hello
summary: 1 ok, 0 changed, 0 skipped, 0 would-change
$ ./gonf -version
0.24.0
```

- `-quiet` keeps only warnings, errors and the summary.
- `-verbose` logs every step (chapter 15).
- `-version` prints the gonf release your gonf was built with.

Reference: [Minimal recipe](../reference.md#minimal-recipe),
[Global flags](../reference.md#global-flags),
[Subcommands](../reference.md#subcommands).

---

← [1. How gonf works](01-how-gonf-works.md) · [Contents](README.md) · Next: [3. Files, directories and links](03-files.md) →
