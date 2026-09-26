# gonf

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
  <img src="assets/logo-light.svg" alt="Gonfy, the gonf beaver, in a yellow hard hat holding a log" width="160">
</picture>

KISS configuration management in plain Go, for personal use, looked after by
Gonfy the beaver. A recipe is a
Go program: it registers tasks, each task declares resources (files,
packages, services, cron jobs, users, ...), and gonf records them into a
versioned plan and applies it, locally or over SSH on Linux, OpenBSD,
FreeBSD and NetBSD hosts. Built with the help of AI, but designed, reviewed
and tested by a human.

## Meet Gonfy

Gonfy is gonf's mascot: a small, cheerful beaver in a yellow hard hat.
A beaver builds its lodge to a plan and, whenever the river moves a stick,
swims out and puts it back. gonf does the same with your hosts. You declare
the lodge once, and every run converges it again, changing only what
drifted. Gonfy guides you through the [tutorial](docs/tutorial/README.md),
where the example recipes manage his portrait, his lodge's message of the
day and a `gonfy` user account.

## Install

> 🦫 **Gonfy says:** One `go install` and I'm ready to build.

```text
go install github.com/snonux/gonf/cmd/gonf@latest   # the demo binary
go get github.com/snonux/gonf@latest                # in your recipe module
```

Build from a checkout with `go build ./...` or `mage build`. Pushing to a
remote host needs Go on the controller: gonf cross-compiles and installs
itself there.

## A minimal recipe

> 🦫 **Gonfy says:** Every lodge starts with one stick. This recipe writes one file, installs one package and names one host to push to.

```go
package main

import (
    . "github.com/snonux/gonf/api"
    "github.com/snonux/gonf/cli"
)

func main() {
    Task("dotfiles", "Shell setup", func() {
        File(Home(".hello"), WithContent("Hi from Gonfy!\n"), WithMode(0o644))
        Package("tmux")
    }, Privileged())

    Host("web", WithSSHUser("rex"), WithSSHHost("web.example"),
        WithPrivilege(PrivilegeDoas))
    cli.Main()
}
```

## Common commands

> 🦫 **Gonfy says:** Preview with `-n` before you gnaw. A dry run changes nothing.

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

> 🦫 **Gonfy says:** New here? Start with the tutorial and I'll show you around the lodge.

- [docs/tutorial/](docs/tutorial/README.md): the tutorial, a book of
  step-by-step chapters with runnable examples, commands and their output.
- [docs/reference.md](docs/reference.md): the quick reference, every
  feature, flag and option.
- [CHANGELOG.md](CHANGELOG.md): release notes.
- [docs/design/](docs/design/README.md): long-form design notes and
  background.
