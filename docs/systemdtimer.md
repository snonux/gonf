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
