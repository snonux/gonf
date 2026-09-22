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
`WithLine` with `WithContent`), is registration-time misuse: the resource is
not registered and a declaration error is reported, which `RecordPlan`, `Run`,
`Apply` and the CLI return (the process is never ended from library code). A
resource rebuilt on the destination from a plan op (`Ensure*`) returns the
same misuse as its apply error. See [plan.md](plan.md), "Error handling
contract".

## Shared resource options

| Option | Meaning |
|--------|---------|
| `DependsOn(res…)` | Topological apply order (in-process via `Apply`, and on the plan path — deps are carried on the wire since plan schema 5) |
| `IsAbsent` | Ensure a resource family that supports absence is gone (`NoFile` / `NoService` / …) |
| `WithSensitive` | Declare the payload secret material the secret scan cannot recognise (transformed values, synced trees): the op is recorded `"sensitive": true` (plan schema 22). File, Dir/SyncDir, ConfigSet and its `ConfigFile` members, Command (with `WithName`, else refused), Package, Cron and SystemdTimer only; Link, Service, Timer, DaemonReload and User carry no payload and refuse it at compile time. See [secrets.md](secrets.md#explicit-sensitivity-withsensitive) |

## Filesystem

See [file-dir-link.md](file-dir-link.md): `WithContent`, `WithSource`,
`WithSourceGlob`, `WithLine`, `WithoutLine`, `WithKeyedLine`, `WithOwner`,
`WithGroup`, `WithMode`, `WithFileMode`, `WithPrune`, `WithSymlink`, `WithHardlink`, and
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
| `WithWatch(ids…)` | Legacy DaemonReload watch ids for `IfChanged` (does not arm; a later `WithWatch` replaces earlier ids, `WithWatch()` clears them); recorded after the `OnChange`/`WatchChanges` ids. `IfChanged, WithWatch(ids…)` records the same op as `WatchChanges(ids…)` |
| `WithCommand` | SystemdTimer: oneshot `ExecStart=` (also Cron) |
| `WithOnCalendar` / `WithOnBootSec` / `WithPersistent` | SystemdTimer schedule |
| `WithDescription` / `WithServiceDescription` | SystemdTimer unit descriptions |
| `WithAfter` / `WithWants` | SystemdTimer service dependencies |

### Change-gate changes after v0.15.0

Unreleased, pre-1.0 (task b72). The recipe DSL is unchanged: `OnChange`,
`WatchChanges`, `IfChanged` and `WithWatch` keep their names, types and
recorded plans (no plan schema change). Deliberate changes:

- A daemon-reload applied directly through `systemd.Ensure` while armed by
  `IfChanged` with neither `WithWatch` ids nor `DependsOn` (a reload that
  could never fire) returns an error instead of being skipped. Registered
  declarations (`DaemonReload`) are unchanged: they may still merge with a
  same-bus declaration, and one that stays unwatchable is refused by the
  plan pre-flight as before.
- A change-gate option applied through the type-erased `Option` path to a
  resource without a change gate names itself in the error:
  `WatchChanges` now says "does not support WatchChanges" (it said
  "OnChange").
- `Service`, `Timer` and `Command` watch lists are de-duplicated.
- Exported Go API below the DSL (no known users): `embed.ChangeGate.Arm`
  and `embed.ChangeGate.HoldsWatching` are removed (use
  `SetChangeWatch(nil)` and `Holds`), `systemd.DaemonReloadResource` no
  longer has `SetIfChanged`, and the capability interface
  `options.ChangeGated` (also `api/options.ChangeGated`) changed from
  `interface{ SetIfChanged() }` to `ChangeWatchable` plus `Watchable`.
  New: `embed.ChangeGate.AddWatch`/`CheckWatch` and
  `options.RecordedChangeGate` (for plan handlers).

### Test seams removed after v0.15.0

Unreleased, pre-1.0 (task 082); test-only Go API, no behaviour or plan
change. The exported `*ForTest` runner and detector setters are gone from the
resource packages (neither client module used them). Tests inside this module
fake host commands through the module-internal `internal/testseam` package
instead (`FakeCommand`, `FakeCrontab`, `FakePackageRunner`,
`FakePackageManager`, `FakeServiceRunner`, `FakeServiceManager`,
`FakeSystemctl`, plus `FakeCrontabLock`; each restores on the test's cleanup
and refuses a parallel test), and capture log lines
with `internal/testutil.CaptureLog`. Removed:

- `cmd.SetRunnersForTest`, `cmd.ResetRunnersForTest`
- `cron.SetRunnersForTest`, `cron.ResetRunnersForTest`
- `pkg.SetRunCmdForTest`, `pkg.ResetRunCmdForTest`,
  `pkg.SetRunCmdWithEnvForTest`, `pkg.ResetRunCmdWithEnvForTest`,
  `pkg.SetDetectPackageManagerForTest`,
  `pkg.ResetDetectPackageManagerForTest`
- `service.SetRunCmdForTest`, `service.ResetRunCmdForTest`,
  `service.SetDetectServiceManagerForTest`,
  `service.ResetDetectServiceManagerForTest`
- `systemd.SetRunCmdForTest`, `systemd.ResetRunCmdForTest`
- `internal/logger.CaptureForTest` (module-internal; replaced by
  `logger.RedirectUnprefixed`, which `testutil.CaptureLog` builds on)

The state resets (`api.ResetForTest`, `resource.ResetForTest`,
`plan.ResetForTest`) stay.

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
