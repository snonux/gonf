# SystemdTimer resource

Declarative install of a Linux **systemd** `.timer` plus a companion oneshot
`.service`. Unlike [`Timer`](timer.md) (enable/start only), `SystemdTimer`
writes the unit files, runs `daemon-reload` when they change, then enables and
starts the timer.

```go
SystemdTimer("backup",
    WithCommand("/usr/local/bin/backup.sh"),
    WithOnCalendar("*-*-* 03:00:00"),
    WithOnBootSec("10min"),
    WithPersistent,
    WithDescription("Nightly backup"),
    WithServiceDescription("Run backup.sh once"),
    WithAfter("network-online.target"),
    WithWants("network-online.target"),
)
NoSystemdTimer("backup")
```

System units land in `/etc/systemd/system/`; `WithUser` uses
`~/.config/systemd/user/`. The `.timer` / `.service` suffix on the name is
optional.

`Run`, `push`, `apply`, and `fleet` record a `systemd_timer` plan op (schema
v7+). See [plan.md](plan.md).

## Sharing the bus's daemon-reload

A `SystemdTimer` declared after a same-bus `SystemdUnits` composition (or an
explicit `DaemonReload`) in the same task shares that bus's reload: the
registered `DaemonReload[user]` / `DaemonReload[system]` op gains a dependency
on the `SystemdTimer`, so the timer applies first and its own change-gated
reload also loads the composition's already-written inputs. A change-gated
reload only fires for a watched change noted after the bus's last reload in
this apply, so the composition's reload is then held unless one of its
inputs changed later. So the bus reloads once for everything written up to
the timer, and every unit is loaded before its own activation: the
composition's activations run after its reload, the timer's
enable/start/restart after the timer's own reload. The timer's activation
now runs before the composition's reload, though: when only the
composition's inputs changed, the timer restarts (with its own, unchanged
units) before those inputs are reloaded. The `systemd_timer` op itself is
unchanged, and no plan schema change is involved.

The timer keeps reloading on its own (at most one extra reload, as before)
when it is alone, on the other bus, declared before the composition, in
another when-block or privilege scope, when it depends on the composition
(joining would close a dependency cycle), or when its `WithAfter` /
`WithWants` name a unit the composition may install (an entry listing several
space-separated units counts each of them): a file input with that
unit's (or its template's) name or drop-in directory, or any input that is
not a single file, such as a `SyncDir` directory. Starting the timer first
could otherwise start such a unit from its stale definition (e.g. a
`Persistent` timer firing on start). The check sees the reload's inputs when
the timer is declared; a later same-bus `SystemdUnits` or `DaemonReload` whose
inputs may install such a unit would merge into the reload the timer already
joined, so that declaration is refused with a `cannot merge` error naming the
timer and the unit. Declare the timer after every same-bus composition instead.

## Options

| Option | Meaning |
|--------|---------|
| `WithCommand` | `ExecStart=` on the oneshot service (required for present) |
| `WithOnCalendar` | `OnCalendar=` (required for present) |
| `WithOnBootSec` | Optional `OnBootSec=` |
| `WithPersistent` | `Persistent=true` |
| `WithDescription` | Timer `[Unit] Description=` (also service fallback) |
| `WithServiceDescription` | Service `[Unit] Description=` |
| `WithAfter` / `WithWants` | Service unit dependencies |
| `WithUser` | User bus + user unit directory |
| `WithRestart` / `WithEnableOnly` | Passed through to the enable/start step |
| `IsAbsent` / `NoSystemdTimer` | Stop/disable and remove both unit files |

Prefer `SystemdTimer` when gonf owns the unit content. Prefer `File` /
`SyncDir` + `DaemonReload` + `Timer` when units come from a tree of sources.
