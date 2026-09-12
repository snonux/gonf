# gonf

<img src="assets/logo-light.svg" alt="Gonf logo" width="140">

Pure-Go syntax KISS configuration management system for personal use. Built with the help of AI, but designed, reviewed and tested by a human.

## Quick notes

- **One apply engine:** tasks are recorded into a versioned JSONL plan, then applied — locally in one shot or remotely after shipping the plan. See [docs/plan.md](docs/plan.md).
- `List(...)` builds a `[]string` for multi-path resources, `Command` args, and `EachKV` pairs (formerly `Elems`).
- Task registration: `Task`, `RegisterMethods`, `Aggregate`, `CLI` — see [docs/tasks.md](docs/tasks.md).
- Path helpers: `Home`, `Expand`, `SyncDir`, `InstallFile`, `SymlinkMap`, … — see [docs/helpers.md](docs/helpers.md).

## CLI cheatsheet

```text
gonf -list
gonf -version
gonf [-n] <task> [task…]           # RecordPlan + Apply locally
gonf plan -o out -id demo <task>…  # write out/plan.jsonl (+ blobs/)
gonf apply [-n] out/plan.jsonl     # apply on this or another host
```

## Docs

Full index: [docs/README.md](docs/README.md)

- [Plan / apply](docs/plan.md) — local one-shot and remote JSONL
- [Tasks, Facts, CLI](docs/tasks.md)
- [File / Dir / Link](docs/file-dir-link.md)
- [Command](docs/command.md)
- [Package](docs/package.md) — OS-auto-detect dnf / pkg_add / pkg / pkgin
- [Service](docs/service.md) — OS-auto-detect systemd / rcctl / FreeBSD+NetBSD `service`
- [Timer](docs/timer.md) — systemd `.timer` units (Linux; `--user` supported)
- [Cron](docs/cron.md) — Puppet-inspired per-user crontab jobs
- [Helpers](docs/helpers.md)
- [Options cheat-sheet](docs/options.md)
- [Replacing `~/git/conf` Rex — missing features](docs/conf-rex-gaps.md)
