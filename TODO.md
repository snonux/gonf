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

DONE! `Dir(..., WithSourceGlob("src/*"), WithFileMode(...), WithPrune)` —
flat Rex-style install of matching regular files into the destination.

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

DONE enough: use `EachKV(List(key, val, ...), ...)` with `Command` +
`Unless` / `ExpectStdout`. No dedicated GitConfig resource.

## 10. In-place file editing (append-line-if-absent)

DONE! `WithLine` / `WithoutLine` on `File` — exact-match idempotent
append/remove (covers `home_tmux_rocky`).

## 11. Tasks, descriptions, and a CLI

DONE! `api.Task` / `Matching` / `Run` / `Tasks` plus `api.CLI`
(`-list`, `-version`, run named tasks). Aggregates via
`Run(Matching("^home_")...)`.

## 12. Nice-to-haves / smaller gaps

DONE!
- Leveled logging via `internal/logger` (`-verbose` / `-quiet`).
- Dry-run (`-dry-run` / `-n`) with WouldChange notes.
- End-of-apply summary (ok / changed / skipped / would-change).
- ~~Dual registries~~ consolidated earlier.

## Suggested implementation order

1. Fix build + split source/target + add file **mode** (sections 0, 1).
2. Directory and symlink resources (sections 2, 3).
3. Glob installs + prune (sections 4, 5).
4. Command/exec resource + OS/host facts (sections 7, 8).
5. Package resource with per-OS backends (section 6).
6. Tasks + CLI (section 11), then git-config / line-in-file / polish
   (sections 9, 10, 12).

