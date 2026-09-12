# Options cheat-sheet

Options live in `github.com/snonux/gonf/api/options`. Unsupported options
`log.Fatalf` at registration time.

## Universal

| Option | Meaning |
|--------|---------|
| `DependsOn(res…)` | Topological apply order |
| `IsAbsent` | Ensure resource is gone (`NoFile` / `NoService` / …) |

## Filesystem

See [file-dir-link.md](file-dir-link.md): `WithContent`, `WithSource`,
`WithSourceGlob`, `WithLine`, `WithoutLine`, `WithOwner`, `WithGroup`,
`WithMode`, `WithFileMode`, `WithPrune`, `WithSymlink`, `WithHardlink`.

## Package

| Option | Meaning |
|--------|---------|
| `IsLatest` | Upgrade/install to latest where the backend supports it |

## Service / Timer

| Option | Meaning |
|--------|---------|
| `WithRestart` | Restart once when already active (Service and Timer) |
| `WithReload` | Reload once when already active (**Service only**; no restart fallback) |
| `WithUser` | `systemctl --user` (systemd Service/Timer only) |

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
