# Options cheat-sheet

Options are implemented in `github.com/snonux/gonf/resource/options`; the
older `github.com/snonux/gonf/api/options` import remains as a re-export.
Resource constructors accept typed family options (`FileOption`, `DirOption`,
`CommandOption`, and so on), so unsupported option/resource pairs fail at
compile time. Shared options implement each family they support.

`options.Option` remains a callable erased compatibility seam. It does not
carry the family check; prefer typed options and constructors. If
an older recipe stores options in `[]options.Option`, convert it explicitly
with the matching `ToFileOptions`, `ToDirOptions`, or other `To*Options`
adapter before passing the slice to a constructor. Because erasure loses the
family marker, adapters cannot validate that a legacy value belongs to the
selected family; use them only for known-compatible legacy slices. Applying an
option the target resource does not support, or an invalid option combination (e.g.
`WithLine` with `WithContent`), still aborts via `logger.Fatal` at registration
time — the fail-fast DSL contract. See [plan.md](plan.md), "Error handling
contract": registration-time misuse fails fast, record- and apply-time
failures return errors.

## Shared resource options

| Option | Meaning |
|--------|---------|
| `DependsOn(res…)` | Topological apply order (in-process via `Apply`, and on the plan path — deps are carried on the wire since plan schema 5) |
| `IsAbsent` | Ensure a resource family that supports absence is gone (`NoFile` / `NoService` / …) |

## Filesystem

See [file-dir-link.md](file-dir-link.md): `WithContent`, `WithSource`,
`WithSourceGlob`, `WithLine`, `WithoutLine`, `WithOwner`, `WithGroup`,
`WithMode`, `WithFileMode`, `WithPrune`, `WithSymlink`, `WithHardlink`, and
`WithName`. A named File keeps its target path but gets the explicit
`File[name]` identity needed when several declarations edit one path.

## Package

| Option | Meaning |
|--------|---------|
| `IsLatest` | Upgrade/install to latest where the backend supports it |
| `WithEnv` | Extra environment (`map[string]string`) for package-manager probes and mutations |

## User

`User` is additive-only; see [user.md](user.md) for creation-only semantics
and backend limits.

| Option | Meaning |
|--------|---------|
| `WithPrimaryGroup` | Primary group for a missing account |
| `WithUserGroup` / `WithSupplementaryGroups` | Add one or several supplementary memberships; never remove existing memberships |
| `WithHome` / `WithCreateHome` | Creation-time home path and optional creation |
| `WithManageHome` | Opt-in: also converge an existing account's passwd home field to `WithHome` (no move, create, or chown; see [user.md](user.md)) |
| `WithShell` / `WithLoginClass` / `WithSystem` | Creation-time account attributes |

## Service / Timer / DaemonReload / SystemdTimer

| Option | Meaning |
|--------|---------|
| `WithRestart` | Restart once when already active (Service, Timer, SystemdTimer) |
| `WithReload` | Reload once when already active (**Service only**; no restart fallback) |
| `WithUser` | `systemctl --user` (systemd Service/Timer/DaemonReload/SystemdTimer) |
| `WithEnableOnly` | Timer/SystemdTimer: enable/disable only (skip start/stop) |
| `OnChange(res…)` | Command: run only when watched resources changed; Service/Timer: still converge state, but fire `WithRestart`/`WithReload` only on a watched change; DaemonReload: reload only on a watched change. It also records ordering dependencies. |
| `WatchChanges(ids…)` | The ids-level `OnChange` (same gate, no ordering dependencies); used by plan handlers and compositions |
| `IfChanged` | Legacy DaemonReload spelling: arms the gate, watching the reload's `DependsOn` ids unless ids are named; prefer `OnChange(res…)` in recipes |
| `WithWatch(ids…)` | Legacy DaemonReload alias of `WatchChanges(ids…)` (arms the gate; calls accumulate) |
| `WithCommand` | SystemdTimer: oneshot `ExecStart=` (also Cron) |
| `WithOnCalendar` / `WithOnBootSec` / `WithPersistent` | SystemdTimer schedule |
| `WithDescription` / `WithServiceDescription` | SystemdTimer unit descriptions |
| `WithAfter` / `WithWants` | SystemdTimer service dependencies |

## Cron

| Option | Meaning |
|--------|---------|
| `WithCommand` | Job command (required for present) |
| `WithCronUser` | Crontab owner (default `root`) |
| `WithMinute` / `WithHour` / `WithMonthday` / `WithMonth` / `WithWeekday` | Schedule |
| `WithCronEnv` | `KEY=VAL` line above the job |
| `WithLegacyCommand` | Remove one exact unmanaged command from a valid cron entry before creating the managed job |

## Command

See [command.md](command.md): `Creates`, `Unless`, `OnlyIf`, `WithName`,
`WithDir`, `WithEnv`, plus `ExpectExit` / `ExpectStdout` on guards.
