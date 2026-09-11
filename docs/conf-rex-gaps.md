# Replacing `~/git/conf` Rex with gonf — missing features

This document compares the Rexfiles under [`~/git/conf`](https://codeberg.org/snonux/conf) (personal fleet CM) with what [gonf](https://github.com/snonux/gonf) can do today.

**Summary:** gonf is a strong fit for *local* configuration (as with the Fedora `dotfiles` consumer). Conf Rex is *remote multi-host* CM aimed at OpenBSD frontends, FreeBSD Garage nodes, and Rocky r-nodes. Closing that gap needs remote execution, BSD package/service backends, and richer templates — not just more File/Dir helpers.

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
gonf today:  gonf task →  local Apply only
```

Without a remote model, gonf cannot replace `rex commons`, `rex garage_deploy`, or r-nodes tasks as used in production.

## Critical gaps

These block a faithful port of conf:

| Conf Rex capability | gonf today | Why it matters |
|---------------------|------------|----------------|
| SSH groups, `user` / `sudo` / `auth for`, `parallelism`, `connection->server` | None | Target frontends, garage, r-nodes |
| `run_task … on => connection->server` | Local `Run` only | Same |
| `pkg` via OpenBSD `pkg_add`, FreeBSD `pkg`, custom `PKG_PATH` | **dnf only** | `base`, DTail, Gogios, etc. |
| `service` / restart (rcctl, systemd) | No Service resource | httpd, relayd, nsd, timers, garage |
| `template(...)` with rich data (maps, arrays, closures, secrets) | `.tmpl` = env + `.Param` only | Most `frontends/*.tpl` |
| Secrets from `./secrets/` (`$secrets`) | No secret helper | Tokens, keys in templates |
| `append_if_no_such_line` | `WithLine` / `WithoutLine` (partial) | Idempotent line append API |
| Rex Cron API (`cron add`) | None | Playground/canary; most prod cron is shell-merged |
| Multi-Rexfile `require` composition | One Go module + `RegisterMethods` | Organizational only (solvable in Go) |

## Workable via `Command` (verbose, not first-class)

Conf already shells out for some of these; gonf can do the same with `Command` + `Unless` / `OnlyIf` / `Creates`, but without dedicated resource semantics:

- Crontab merge (`crontab -l | grep -v …; append; crontab`) — rsync, nsd failover, pf exporter, gogios
- Ad-hoc `run` + `unless` probes
- `systemctl` / `rcctl` / `doas`
- User creation (`adduser` / `usermod` + guards) for `_dserver`, `_gogios`
- Custom package URL installs
- Local write + remote `doas install` (garage pattern)
- “If any of these files changed, reload once” (r-nodes `$changed` + `daemon-reload`) — gonf notes change status but has no fan-in helper

## Already covered for *local* apply

Ignoring remotes, these map reasonably:

- File / dir install, modes, owner/group
- Line ensure / absent
- Command guards
- Task registration, aggregates, `-list` / dry-run
- Symlinks (gonf is ahead of conf Rex here)

The **dotfiles** laptop port shows that local Linux home/pkg workflows are in good shape. Conf is a different problem.

## Minimum feature set to replace conf Rex

1. **Remote execution** — SSH inventory, per-group auth/sudo, parallel apply
2. **Package backends** — OpenBSD `pkg_add`, FreeBSD `pkg`, custom repo/`PKG_PATH`
3. **Service resource** (or strong conventions) for OpenBSD rc and systemd
4. **Richer templates** — arbitrary data/functions, not only process env
5. **Secrets loading** convention (files under a secrets dir, never committed)
6. Nice-to-have: **Cron** resource or crontab-merge helper; **on_change fan-in** for one reload after many file updates

Without items 1–4, `frontends/Rexfile` cannot be replaced meaningfully.

## Non-gaps / out of scope

- `conf/rcm` is unrelated to Rex
- Unused NetBSD/FreeBSD dserver templates in frontends
- `playground/Rexfile` cron canary is experimental

## Related

- Consumer that *does* fit gonf today: `~/git/dotfiles/gonf` (Fedora laptop)
- Conf Justfile wrappers (e.g. garage) call `rex` today; they would call `gonf` only after remote support exists
