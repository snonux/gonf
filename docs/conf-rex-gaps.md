# Replacing `~/git/conf` Rex with gonf — missing features

This document compares the Rexfiles under [`~/git/conf`](https://codeberg.org/snonux/conf) (personal fleet CM) with what [gonf](https://github.com/snonux/gonf) can do today.

**Summary:** gonf is a strong fit for *local* configuration (as with the Fedora `dotfiles` consumer). Conf Rex is *remote multi-host* CM aimed at OpenBSD frontends, FreeBSD Garage nodes, and Rocky r-nodes.

**Local package and service management is in place** — see [package.md](package.md) and [service.md](service.md): `Package` / `NoPackage` (dnf, OpenBSD `pkg_add`, FreeBSD `pkg`, NetBSD `pkgin`) and `Service` / `NoService` (systemd, `rcctl`, FreeBSD/NetBSD `service`). Closing the conf gap now needs **remote execution**, richer templates, and secrets — not more pkg/service backends.

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

Without a remote model, gonf cannot replace `rex commons`, `rex garage_deploy`, or r-nodes tasks as used in production — even though the local pkg/service resources already match those OSes.

## Critical gaps

These block a faithful port of conf:

| Conf Rex capability | gonf today | Why it matters |
|---------------------|------------|----------------|
| SSH groups, `user` / `sudo` / `auth for`, `parallelism`, `connection->server` | None | Target frontends, garage, r-nodes |
| `run_task … on => connection->server` | Local `Run` only | Same |
| `pkg` via OpenBSD `pkg_add`, FreeBSD `pkg`, custom `PKG_PATH` | **Done locally:** `Package` / `NoPackage` auto-detects dnf / OpenBSD / FreeBSD / NetBSD; custom `PKG_PATH` still manual | Remote still required for conf fleet |
| `service` / restart (rcctl, systemd, FreeBSD/NetBSD `service`) | **Done locally:** `Service` / `NoService` (+ `WithRestart` / `WithReload` / `WithUser`); see [service.md](service.md) | Remote still required for conf fleet |
| `template(...)` with rich data (maps, arrays, closures, secrets) | `.tmpl` = env + `.Param` only | Most `frontends/*.tpl` |
| Secrets from `./secrets/` (`$secrets`) | No secret helper | Tokens, keys in templates |
| `append_if_no_such_line` | `WithLine` / `WithoutLine` (partial) | Idempotent line append API |
| Rex Cron API (`cron add`) | **Done locally:** `Cron` / `NoCron` (per-user + root crontab markers); see [cron.md](cron.md) | Remote still required for conf fleet |
| Multi-Rexfile `require` composition | One Go module + `RegisterMethods` | Organizational only (solvable in Go) |

## Workable via `Command` (verbose, not first-class)

Conf already shells out for some of these; gonf can do the same with `Command` + `Unless` / `OnlyIf` / `Creates`, but without dedicated resource semantics:

- Ad-hoc `run` + `unless` probes
- User creation (`adduser` / `usermod` + guards) for `_dserver`, `_gogios`
- Custom package URL installs / non-default `PKG_PATH`
- Local write + remote `doas install` (garage pattern)
- “If any of these files changed, reload once” (r-nodes `$changed` + `daemon-reload`) — gonf notes change status but has no fan-in helper

Do **not** use `Command` for ordinary package install or service enable/start — use `Package` / `Service` instead.

## Already covered for *local* apply

Ignoring remotes, these map reasonably:

- File / dir install, modes, owner/group
- Line ensure / absent
- Command guards
- Task registration, aggregates, `-list` / dry-run
- Symlinks (gonf is ahead of conf Rex here)
- **Package** — dnf / OpenBSD `pkg_add` / FreeBSD `pkg` / NetBSD `pkgin` ([package.md](package.md))
- **Service** — systemd / `rcctl` / FreeBSD+NetBSD `service` ([service.md](service.md))
- **Cron** — per-user and root crontab markers ([cron.md](cron.md))

The **dotfiles** laptop port shows that local Linux home/pkg workflows are in good shape. Conf is a different problem (remote + templates).

## Minimum feature set to replace conf Rex

1. **Remote execution** — SSH inventory, per-group auth/sudo, parallel apply (**still the main blocker**)
2. **Package backends** — **done for local apply** (`Package` on Linux/OpenBSD/FreeBSD/NetBSD); custom repo/`PKG_PATH` optional; still need remote for conf fleet
3. **Service resource** — **done for local apply** (`Service` on systemd / `rcctl` / FreeBSD / NetBSD); still need remote for conf fleet
4. **Richer templates** — arbitrary data/functions, not only process env
5. **Secrets loading** convention (files under a secrets dir, never committed)
6. Nice-to-have: **on_change fan-in** for one reload after many file updates

Without item 1 (and 4 for most frontend templates), `frontends/Rexfile` cannot be replaced meaningfully. Local Package/Service/Cron are already available.

## Non-gaps / out of scope

- `conf/rcm` is unrelated to Rex
- Unused NetBSD/FreeBSD dserver templates in frontends
- `playground/Rexfile` cron canary is experimental (gonf `Cron` covers the same idea locally)

## Related

- Consumer that *does* fit gonf today: `~/git/dotfiles/gonf` (Fedora laptop)
- Conf Justfile wrappers (e.g. garage) call `rex` today; they would call `gonf` only after remote support exists
- Docs: [package.md](package.md), [service.md](service.md), [cron.md](cron.md)
