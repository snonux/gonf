# gonf

<img src="assets/logo-light.svg" alt="Gonf logo" width="140">

KISS configuration management in plain Go, for personal use. A recipe is a
Go program: it registers tasks, each task declares resources (files,
packages, services, cron jobs, users, ...), and gonf records them into a
versioned plan and applies it, locally or over SSH on Linux, OpenBSD,
FreeBSD and NetBSD hosts. Built with the help of AI, but designed, reviewed
and tested by a human.

## Install

```text
go install github.com/snonux/gonf/cmd/gonf@latest   # the demo binary
go get github.com/snonux/gonf@latest                # in your recipe module
```

Build from a checkout with `go build ./...` or `mage build`. Pushing to a
remote host needs Go on the controller: gonf cross-compiles and installs
itself there.

## A minimal recipe

```go
package main

import (
    "os"

    . "github.com/snonux/gonf/api"
    . "github.com/snonux/gonf/api/options"
    "github.com/snonux/gonf/cli"
)

func main() {
    Task("dotfiles", "Shell setup", func() {
        File(Home(".hello"), WithContent("hi\n"), WithMode(0o644))
        Package("tmux")
    }, Privileged())

    Host("web", WithSSHUser("rex"), WithSSHHost("web.example"),
        WithPrivilege(PrivilegeDoas))
    os.Exit(cli.CLI())
}
```

## Common commands

```text
gonf -list                               # registered tasks
gonf -n -privilege=sudo dotfiles         # dry run locally
gonf -privilege=sudo dotfiles            # apply locally
gonf push -privilege=doas rex@web dotfiles
gonf cluster -preview edge base          # strict remote preview
gonf fleet -j 2 homelab base             # parallel push
gonf plan -o out dotfiles                # write out/plan.jsonl
gonf apply out/plan.jsonl                # apply it on any host with gonf
gonf plan -o out -seal dotfiles          # age-encrypted out/plan.age
gonf apply -identity ~/.config/gonf/identity out/plan.age
```

## Docs

- [docs/reference.md](docs/reference.md): the quick reference, every
  feature, flag and option.
- [CHANGELOG.md](CHANGELOG.md): release notes.
- [docs/design/](docs/design/README.md): long-form design notes and
  background.
