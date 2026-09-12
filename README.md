# gonf

<img src="assets/logo-light.svg" alt="Gonf logo" width="140">

Pure-Go syntax KISS configuration management system for personal use. Built with the help of AI, but designed, reviewed and tested by a human.

## Quick notes

- `List(...)` builds a `[]string` for multi-path resources, `Command` args, and `EachKV` pairs (formerly `Elems`).
- Task registration: `Task`, `RegisterMethods`, `Aggregate`, `CLI` — see [docs/tasks.md](docs/tasks.md).
- Path helpers: `Home`, `Expand`, `SyncDir`, `InstallFile`, `SymlinkMap`, … — see [docs/helpers.md](docs/helpers.md).

## Docs

Full index: [docs/README.md](docs/README.md)

- [File / Dir / Link](docs/file-dir-link.md)
- [Command](docs/command.md)
- [Package](docs/package.md) — OS-auto-detect dnf / pkg_add / pkg / pkgin
- [Service](docs/service.md) — OS-auto-detect systemd / rcctl / FreeBSD+NetBSD `service`
- [Timer](docs/timer.md) — systemd `.timer` units (Linux; `--user` supported)
- [Cron](docs/cron.md) — Puppet-inspired per-user crontab jobs
- [Tasks, Facts, CLI](docs/tasks.md)
- [Helpers](docs/helpers.md)
- [Options cheat-sheet](docs/options.md)
- [Replacing `~/git/conf` Rex — missing features](docs/conf-rex-gaps.md)
