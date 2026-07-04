# TODO — Features needed to replace the dotfiles Rexfile

This document lists the features `gonf` needs before it can replace the
Perl [Rex](https://www.rexify.org/) `Rexfile` used to install
`~/git/dotfiles`. It is derived from an audit of that Rexfile
(`~/git/dotfiles/Rexfile`).

## 1. File resource — missing capabilities

The Rexfile uses `file` for far more than "write these bytes". gonf's
`file.Have` currently only manages content + checksum idempotency. Missing:

- **`ensure => 'absent'`.** Remove a file if present. Used by `prune_dir`.
- **`ensure => 'directory'`.** See section 2.

## 2. Directory resource

Rexfile creates directories with a mode all over the place
(`~/.config/*`, `~/scripts`, `~/QuickEdit`, `~/.config/systemd/user`, agent
tool dirs). gonf has no directory concept. Need:

- `Have`-style directory resource: create if missing, enforce mode,
  idempotent, register in the resource registry.

## 3. Symlink resource

The Rexfile does a lot of symlink management, none of which gonf supports:

- fish `conf.d` → `~/.config/fish/conf.d` (with rename-to-`.old` fallback).
- gitsyncer config dir symlink.
- Agent tool dirs: `~/.cursor`, `~/.claude`, `~/.agents`, `~/.opencode`,
  `~/.pi`, `~/.amp`, `~/.codex` each get `commands`/`skills`/`prompts`
  symlinks into `~/Notes/Prompts/...`.
- `~/QuickEdit/*` symlinks to many source dirs.

Needs a symlink resource that:

- Creates a symlink to a target.
- Is idempotent: leaves it alone if it already points at the right place.
- Repoints if it points elsewhere.
- Refuses (or has an explicit policy) to clobber a real file/dir; supports the
  Rexfile's rename-existing-dir-to-`.old` behavior where needed.

## 4. Glob / multi-file installs

The `ensure_dir` helper installs `"$DOT/foo/*"` into a destination dir. gonf
`file.Have` handles a single file. Need a way to install a glob of source files
into a destination directory (helix, ghostty, hexai, lazygit, opencode, tmux,
sway, waybar, scripts, systemd units, calendar, pipewire).

## 5. Prune / reconcile stale files

`prune_dir` removes regular files in a destination whose basename is not in the
source glob (used for `~/scripts`), while leaving dotfiles and subdirectories
untouched. gonf needs a prune/reconcile operation so removed source files also
disappear from the destination.

## 6. Package resource (multi-OS)

The biggest missing piece. Rexfile has `pkg_termux`, `pkg_freebsd`,
`pkg_fedora`, `pkg_rocky` tasks, each installing a package list via
`pkg $name, ensure => 'installed'`. gonf has no package management. Need:

- A package resource with idempotent "installed" semantics.
- Backends per platform: Termux (`pkg`), FreeBSD (`pkg`),
  Fedora/Rocky (`dnf`), and ideally room for apt/brew later.
- Ability to declare per-OS package lists.

## 7. Command execution (`run`)

Rexfile shells out with `run "..."` for things gonf can't currently do:

- `git config --global ...` (idempotent global git config, section 9).
- `systemctl --user daemon-reload`, `systemctl --user enable <timer>`.

Need a command/exec resource (run a command, capture output, optional
idempotency guard / "unless" condition).

## 8. OS and host detection / conditionals

Rexfile branches on:

- OS: `$^O eq 'linux' | 'darwin' | 'freebsd'` (hexai, zsh, gitconfig,
  quickedit, tmux_rocky).
- Hostname: `hostname =~ /rocky/` for tmux rocky overrides.

gonf needs runtime facts (OS, hostname, maybe distro) and a clean way for
config to branch on them.

## 9. Idempotent key/value config (git config)

`home_gitconfig` sets ~13 `git config --global` keys. Ideally gonf offers a
resource that sets a key to a value idempotently (read current, set if
different) rather than always shelling out. Minimum: implement via the command
resource (section 7); nice-to-have: a dedicated git-config resource.

## 10. In-place file editing (append-line-if-absent)

`home_tmux_rocky` edits existing files: removes a stale `source-file` line from
`tmux.local.conf` and appends a `source-file` line to the end of `tmux.conf`
only if not already present. Need a "line in file" style resource
(ensure line present/absent, idempotent) to cover this.

## 11. Tasks, descriptions, and a CLI

Rexfile is organized into named tasks with `desc`, an aggregate `home` task
that runs every `home_*` task, and Rex's CLI to run a chosen task. gonf today
just calls a hardcoded `examples.Run()`. Need:

- A way to declare named units of work (tasks/groups) with descriptions.
- CLI to list tasks and run one, several, or all
  (replace the `--version`-only flag handling in `cmd/gonf/main.go`, which is
  also currently buggy — it reads `*version` before `flag.Parse()`).
- An aggregate/dependency mechanism (the `home` = all `home_*` pattern), which
  ties into the existing `dependsOn` field on `Resource` that is currently
  unused.

## 12. Nice-to-haves / smaller gaps

- Structured logging with levels (Rexfile uses `Rex::Logger::info/warn`);
  gonf uses stdlib `log` with verbose per-file debug lines — consider levels
  and quieter default output.
- Dry-run / "diff" mode to preview changes before applying.
- Report of what changed vs. what was already in the desired state.
- `resources` registry vs. `resource` repository are currently **two separate
  registries** doing the same job (`internal/resources/resources.go` and
  `internal/resource/repository.go`). Consolidate to one.

## Suggested implementation order

1. Fix build + split source/target + add file **mode** (sections 0, 1).
2. Directory and symlink resources (sections 2, 3).
3. Glob installs + prune (sections 4, 5).
4. Command/exec resource + OS/host facts (sections 7, 8).
5. Package resource with per-OS backends (section 6).
6. Tasks + CLI (section 11), then git-config / line-in-file / polish
   (sections 9, 10, 12).
