# 2. Your first recipe

A recipe is a normal Go module. In this chapter you create one, add a task
that writes a file into your home directory, and run it a few times to see
how gonf reports changes.

## Create the module

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
	Task("hello", "Write ~/hello.txt", func() {
		File(DestHome("hello.txt"), WithContent("Hello from gonf!\n"), WithMode(0o644))
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
$ go build -o recipe .
```

## List, preview, apply

`-list` prints every task with its description:

```text
$ ./recipe -list
hello	Write ~/hello.txt
```

`-n` (or `-dry-run`) shows what would change, without changing anything:

```text
$ ./recipe -n hello
2026/09/26 08:25:12 dry-run: would update /home/paul/hello.txt
summary: 0 ok, 0 changed, 0 skipped, 1 would-change
  would-change File[/home/paul/hello.txt]
```

Every resource has an **ID** such as `File[/home/paul/hello.txt]`. The
summary lists the IDs of everything that is not `ok`.

Now apply it by naming the task:

```text
$ ./recipe hello
2026/09/26 08:25:12 updated /home/paul/hello.txt
summary: 0 ok, 1 changed, 0 skipped, 0 would-change
  changed File[/home/paul/hello.txt]
$ cat ~/hello.txt
Hello from gonf!
```

## Run it again

```text
$ ./recipe hello
summary: 1 ok, 0 changed, 0 skipped, 0 would-change
```

Nothing changed, because the file already matches. Now change the file
behind gonf's back and look at the drift:

```text
$ echo tampered > ~/hello.txt
$ ./recipe -n hello
2026/09/26 08:25:12 dry-run: would update /home/paul/hello.txt
summary: 0 ok, 0 changed, 0 skipped, 1 would-change
  would-change File[/home/paul/hello.txt]
$ ./recipe hello
2026/09/26 08:25:12 updated /home/paul/hello.txt
summary: 0 ok, 1 changed, 0 skipped, 0 would-change
  changed File[/home/paul/hello.txt]
```

This is the core loop of gonf: declare, preview with `-n`, apply, and run
again whenever you like.

## A few more flags

```text
$ ./recipe -quiet hello
summary: 1 ok, 0 changed, 0 skipped, 0 would-change
$ ./recipe -version
0.24.0
```

- `-quiet` keeps only warnings, errors and the summary.
- `-verbose` logs every step (chapter 15).
- `-version` prints the gonf release your recipe was built with.

Reference: [Minimal recipe](../reference.md#minimal-recipe),
[Global flags](../reference.md#global-flags),
[Subcommands](../reference.md#subcommands).

---

← [1. How gonf works](01-how-gonf-works.md) · [Contents](README.md) · Next: [3. Files, directories and links](03-files.md) →
