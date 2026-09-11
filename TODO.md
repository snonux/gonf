# TODO — Features needed to replace the dotfiles Rexfile

This document lists the features `gonf` needs before it can replace the
Perl [Rex](https://www.rexify.org/) `Rexfile` used to install
`~/git/dotfiles`. It is derived from an audit of that Rexfile
(`~/git/dotfiles/Rexfile`).

## 1. File resource — missing capabilities

DONE!

## 2. Directory resource

DONE! 

## 3. Symlink resource

DONE!

## 4. Glob / multi-file installs

MAYBE LATER, JUST USE NATIVE GO GLOB FOR NOW!

## 5. Prune / reconcile stale files

DONE!

## 6. Package resource (multi-OS)

DONE (for Fedora!)

## 7. Command execution (`run`)

DONE! `api.Command` with `Unless` / `OnlyIf` / `Creates` / `WithDir` /
`WithEnv` / `WithName`. Covers Rexfile `git config`, `systemctl`, etc.

## 8. OS and host detection / conditionals

DONE! Go can do that natively relatively easily!

## 9. Idempotent key/value config (git config)

we will see!

## 10. In-place file editing (append-line-if-absent)

DONE! `WithLine` / `WithoutLine` on `File` — exact-match idempotent
append/remove (covers `home_tmux_rocky`).

## 11. Tasks, descriptions, and a CLI

DONE! `api.Task` / `Matching` / `Run` / `Tasks` plus `api.CLI`
(`-list`, `-version`, run named tasks). Aggregates via
`Run(Matching("^home_")...)`.

## 12. Nice-to-haves / smaller gaps

- Structured logging with levels (Rexfile uses `Rex::Logger::info/warn`);
  gonf uses stdlib `log` with verbose per-file debug lines — consider levels
  and quieter default output.
- Dry-run / "diff" mode to preview changes before applying.
- Report of what changed vs. what was already in the desired state.
- ~~`resources` registry vs. `resource` repository were **two separate
  registries** doing the same job.~~ Consolidated: the `resources` package
  (with its never-called `Register` and no-op `Init`) was removed; the
  resource repository initializes lazily on first use.

## Suggested implementation order

1. Fix build + split source/target + add file **mode** (sections 0, 1).
2. Directory and symlink resources (sections 2, 3).
3. Glob installs + prune (sections 4, 5).
4. Command/exec resource + OS/host facts (sections 7, 8).
5. Package resource with per-OS backends (section 6).
6. Tasks + CLI (section 11), then git-config / line-in-file / polish
   (sections 9, 10, 12).

