# gonf

<img src="assets/logo-light.svg" alt="Gonf logo" width="140">

Pure-Go syntax KISS configuration management system for personal use. Built with the help of AI, but designed, reviewed and tested by a human.

## Quick notes

- `List(...)` builds a `[]string` for multi-path resources, `Command` args, and `EachKV` pairs (formerly `Elems`).
- Task registration: `Task`, `RegisterMethods`, `Aggregate`, `CLI`.
- Path helpers: `Home`, `Expand`, `SyncDir`, `InstallFile`, `SymlinkMap`, …

## Docs

- [Package resource](docs/package.md) — OS-auto-detect dnf / pkg_add / pkg / pkgin
- [Service resource](docs/service.md) — OS-auto-detect systemd / rcctl / FreeBSD+NetBSD `service`
- [Timer resource](docs/timer.md) — systemd `.timer` units (Linux; `--user` supported)
- [Cron resource](docs/cron.md) — Puppet-inspired per-user crontab jobs
- [Replacing `~/git/conf` Rex — missing features](docs/conf-rex-gaps.md)
