# Replacing `~/git/conf` Rex with gonf — missing features

This document compares the Rexfiles under [`~/git/conf`](https://codeberg.org/snonux/conf) (personal fleet CM) with what [gonf](https://github.com/snonux/gonf) can do today.

**Summary:** gonf is a strong fit for *local* configuration (as with the Fedora `dotfiles` consumer) and can **push** plans over SSH to one host or a **named fleet** in parallel, with **Task `Privileged()` + Host `WithPrivilege(sudo|doas)`** split apply ([plan.md](plan.md)). Conf Rex is still ahead on rich templates and secrets.

**Local package and service management is in place** — see [package.md](package.md) and [service.md](service.md). Closing the conf gap now needs **privilege escalation**, richer templates, and secrets — not more pkg/service backends.

## Conf Rexfile inventory

| Path | Role |
|------|------|
| `Rexfile` | Aggregator (`require` of child Rexfiles) |
| `frontends/Rexfile` | Main OpenBSD frontend fleet (~30+ tasks) |
| `f3s/garage/Rexfile` | FreeBSD Garage config deploy |
| `f3s/r-nodes/Rexfile` | Rocky/k3s host units and monitors |
| `playground/Rexfile` | Cron API experiment |

## Architecture mismatch

```text
Rex (conf):  rex task  →  SSH groups / sudo  →  remote file|pkg|service|run
gonf today:  gonf fleet →  Host/Fleet inventory → parallel GONF-PUSH/1 over ssh
             gonf push  →  one host → gonf apply -
             gonf plan  →  JSONL (+blobs) → gonf apply (manual ship)
             gonf task  →  RecordPlan+Apply locally (same engine)
```

gonf can produce a portable plan and apply it anywhere gonf runs. **`Host` /
`Fleet`** register SSH inventory in Go; **`gonf fleet`** / `PushFleet` fan out
in parallel. It still lacks Rex-style **sudo/doas** — the SSH user must already
be able to apply as themselves.

## Critical gaps

These block a faithful port of conf:

| Conf Rex capability | gonf today | Why it matters |
|---------------------|------------|----------------|
| SSH groups, `user` / `sudo` / `auth for`, `parallelism`, `connection->server` | **Done for groups/parallel/user;** `WithPrivilege(sudo\|doas\|none)` + `Privileged()` tasks (no interactive passwords) | Target frontends, garage, r-nodes |
| `run_task … on => connection->server` | `gonf fleet <name> <tasks…>` or `PushHost` / `gonf push` | Same |
| `pkg` via OpenBSD `pkg_add`, FreeBSD `pkg`, custom `PKG_PATH` | **Done locally / in plans:** `Package` / `NoPackage`; custom `PKG_PATH` still manual | Fleet still needs transport |
| `service` / restart (rcctl, systemd, FreeBSD/NetBSD `service`) | **Done locally / in plans:** `Service` / `NoService` | Fleet still needs transport |
| `template(...)` with rich data (maps, arrays, closures, secrets) | `.tmpl` = env + `.Param` only | Most `frontends/*.tpl` |
| Secrets from `./secrets/` (`$secrets`) | No secret helper | Tokens, keys in templates |
| `append_if_no_such_line` | `WithLine` / `WithoutLine` (partial) | Idempotent line append API |
| Rex Cron API (`cron add`) | **Done locally / in plans:** `Cron` / `NoCron` | Fleet still needs transport |
| Multi-Rexfile `require` composition | One Go module + `RegisterMethods` | Organizational only (solvable in Go) |

## Workable via `Command` (verbose, not first-class)

Conf already shells out for some of these; gonf can do the same with `Command` + `Unless` / `OnlyIf` / `Creates`, but without dedicated resource semantics:

- Ad-hoc `run` + `unless` probes
- User creation (`adduser` / `usermod` + guards) for `_dserver`, `_gogios`
- Custom package URL installs / non-default `PKG_PATH`
- Local write + remote `doas install` (garage pattern)
- “If any of these files changed, reload once” — **done:** `DaemonReload(..., DependsOn(units), IfChanged)` ([service.md](service.md), [timer.md](timer.md))

Do **not** use `Command` for ordinary package install or service enable/start — use `Package` / `Service` instead.

## Already covered for *local* / plan apply

Ignoring SSH orchestration, these map reasonably:

- File / dir install, modes, owner/group
- Line ensure / absent
- Command guards
- Task registration, aggregates, `-list` / dry-run
- Symlinks (gonf is ahead of conf Rex here)
- **Plan serialize + apply** — versioned JSONL, condition recipes, content blobs ([plan.md](plan.md))
- **Package** — dnf / OpenBSD `pkg_add` / FreeBSD `pkg` / NetBSD `pkgin` ([package.md](package.md))
- **Service** — systemd / `rcctl` / FreeBSD+NetBSD `service` ([service.md](service.md))
- **Cron** — per-user and root crontab markers ([cron.md](cron.md))

The **dotfiles** laptop port shows that local Linux home/pkg workflows are in good shape. Conf still needs fleet SSH + templates.

## Minimum feature set to replace conf Rex

1. **Remote orchestration** — **Host/Fleet + parallel push + privilege split done** ([plan.md](plan.md)); interactive sudo passwords still out of scope
2. **Package backends** — **done** for plan/local apply; custom repo/`PKG_PATH` optional
3. **Service resource** — **done** for plan/local apply
4. **Richer templates** — arbitrary data/functions, not only process env
5. **Secrets loading** convention (files under a secrets dir, never committed)
6. Nice-to-have: **on_change fan-in** for one reload after many file updates

Without item 4, many `frontends/*.tpl` cannot be replaced fully. Inventory, parallel push, and privilege split apply are available.

## Non-gaps / out of scope

- `conf/rcm` is unrelated to Rex
- Unused NetBSD/FreeBSD dserver templates in frontends
- `playground/Rexfile` cron canary is experimental (gonf `Cron` covers the same idea locally)

## Related

- Consumer that *does* fit gonf today: `~/git/dotfiles/gonf` (Fedora laptop)
- Conf Justfile wrappers (e.g. garage) call `rex` today; they would call `gonf` only after remote support exists
- Full feature docs: [README.md](README.md) (index), [package.md](package.md), [service.md](service.md), [cron.md](cron.md), [timer.md](timer.md)
