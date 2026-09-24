# gonf

<img src="assets/logo-light.svg" alt="Gonf logo" width="140">

Pure-Go syntax KISS configuration management system for personal use. Built with the help of AI, but designed, reviewed and tested by a human.

## Quick notes

- **One apply engine:** tasks are recorded into a versioned JSONL plan, then applied — locally in one shot or remotely after shipping the plan. See [docs/plan.md](docs/plan.md).
- `List(...)` builds a `[]string` for multi-path resources, `Command` args, and `EachKV` pairs (formerly `Elems`).
- Task registration: `Task`, `RegisterMethods`, `Aggregate`, `AggregateTasks`, `Alias`, `CLI` — see [docs/tasks.md](docs/tasks.md).
- Path helpers: `Home`, `Expand`, `SyncDir`, `InstallFile`, `SymlinkMap`, … — see [docs/helpers.md](docs/helpers.md).

## CLI cheatsheet

```text
gonf -list
gonf -version
gonf [-n] <task> [task…]           # RecordPlan + Apply locally
gonf plan -o out -id demo <task>…  # write out/plan.jsonl (+ blobs/); out is yours, not world-writable,
                                   # not group-writable except by your private group (a UPG 0775 checkout is fine; root has none);
                                   # a plan carrying secrets is sealed to out/plan.age by default when
                                   # ~/.config/gonf/recipients exists (-plaintext opts out; docs/plan-encryption.md)
gonf plan -stdout <task>…          # print plan JSONL to stdout (refused when it carries secrets,
                                   # unless -with-secrets; see docs/secrets.md)
gonf plan -redacted <task>…        # print a redacted, non-replayable preview
gonf plan -o out -seal <task>…     # age-encrypt to out/plan.age for -recipient r… + the recipients file
                                   # (prints recipient fingerprints; full keys with top-level -verbose)
gonf plan -o out -seal -for <host|cluster|fleet> <task>…  # one out/plan-<host>.age per host, all or nothing
gonf plan-signer-keygen signer     # new Ed25519 signer key; prints its trusted-signers line
gonf plan -o out -seal -sign signer <task>…  # sign what -seal writes (docs/plan-signing.md)
gonf apply [-n] out/plan.jsonl     # apply on this or another host
gonf apply -identity key out/plan.age        # decrypt + apply (root must pass -identity)
gonf apply -trusted-signers f -require-signed out/plan.age  # verify signature and age (-max-signed-age 24h) first
gonf plan-verify -trusted-signers f out/plan.age | age -d -i key | gonf apply -  # emergency path
gonf apply -                       # apply GONF-PUSH/1, JSONL or a sealed/signed plan from stdin
gonf push [-n] user@host <task>…   # RecordPlan in memory → ssh → apply -
gonf cluster [-n|-preview] <cluster> <task>…  # record once, parallel push to each host
gonf hosts / clusters / fleets     # list Host/Cluster/Fleet inventory
gonf fleet [-n] [-j N] [-host-timeout 10m] <fleet> <task>…  # parallel push; per-host timeout
gonf -privilege=doas <tasks…>      # Privileged() tasks via doas gonf apply
gonf push -privilege=sudo user@host <task>…  # Privileged() tasks over ssh
                                   # (push: -privilege=none + Privileged() is an error)
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
- [Secrets](docs/secrets.md) — `MustSecret`, providers, `secret.NewFallback`, foostore
- [Sealed plans](docs/plan-encryption.md) and [signed plans](docs/plan-signing.md)
- [Replacing `~/git/conf` Rex — missing features](docs/conf-rex-gaps.md)
- [Release notes](CHANGELOG.md)
