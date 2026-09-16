# Options cheat-sheet

Options live in `github.com/snonux/gonf/api/options`. Applying an option the
target resource does not support, or an invalid option combination (e.g.
`WithLine` with `WithContent`), aborts via `logger.Fatal` at registration
time — the fail-fast DSL contract. See [plan.md](plan.md), "Error handling
contract": registration-time misuse fails fast, record- and apply-time
failures return errors.

## Universal

| Option | Meaning |
|--------|---------|
| `DependsOn(res…)` | Topological apply order (in-process via `Apply`, and on the plan path — deps are carried on the wire since plan schema 5) |
| `IsAbsent` | Ensure resource is gone (`NoFile` / `NoService` / …) |

## Filesystem

See [file-dir-link.md](file-dir-link.md): `WithContent`, `WithSource`,
`WithSourceGlob`, `WithLine`, `WithoutLine`, `WithOwner`, `WithGroup`,
`WithMode`, `WithFileMode`, `WithPrune`, `WithSymlink`, `WithHardlink`.

## Package

| Option | Meaning |
|--------|---------|
| `IsLatest` | Upgrade/install to latest where the backend supports it |

## Service / Timer / DaemonReload / SystemdTimer

| Option | Meaning |
|--------|---------|
| `WithRestart` | Restart once when already active (Service, Timer, SystemdTimer) |
| `WithReload` | Reload once when already active (**Service only**; no restart fallback) |
| `WithUser` | `systemctl --user` (systemd Service/Timer/DaemonReload/SystemdTimer) |
| `WithEnableOnly` | Timer/SystemdTimer: enable/disable only (skip start/stop) |
| `IfChanged` | DaemonReload: skip unless a DependsOn/WithWatch target changed |
| `WithWatch(ids…)` | DaemonReload: explicit ids for IfChanged (plan/Ensure) |
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

## Command

See [command.md](command.md): `Creates`, `Unless`, `OnlyIf`, `WithName`,
`WithDir`, `WithEnv`, plus `ExpectExit` / `ExpectStdout` on guards.
