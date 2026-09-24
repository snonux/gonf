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
| `DependsOn(res…)` | Topological apply order via the plan engine (deps are carried on the wire since plan schema 5) |
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

Removed in v0.16.0 (task b72). The recipe DSL is unchanged: `OnChange`,
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

Removed in v0.16.0 (task 082); test-only Go API, no behaviour or plan
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

### Per-apply runner injection added (task qb2), preferred going forward

Added in v0.16.6+ (task qb2). `internal/testseam`'s process-global fakes
above are no longer the only test seam: a migrated resource kind's backend
runner instead travels through `plan.ApplyContext.Runners`
(`*internal/runners.Set`), populated per apply from the `context.Context`
passed to `plan.ApplyWithContext`/`ApplyPlan`
(`internal/runners.WithSet`/`FromContext`, an unexported context key an
external recipe module can never populate — it only ever observes the nil
default, i.e. "use the real runner"). Two narrow module-internal hooks carry
a `*runners.Set` into one whole apply for a test that cannot build
`plan.ApplyContext` itself: `internal/testapply.ApplyWithRunners` and, for
`api`'s own package and its tests, its unexported `applyWithRunners`
(`api.Apply` calls it with `nil`). This is the PREFERRED mechanism for any
newly migrated kind; `internal/testseam` is not being extended further and
should shrink as each kind converts. See `AGENTS.md`, "Test seams", for the
full contract (the `newXWith`/`ensureWith` per-kind constructor shape,
which hook to use from which kind of test) and `docs/plan.md`'s "Test seams
note" for the `ApplyContext.Runners` field itself.

`resource/cmd` (the `command` plan kind) migrated first (task qb2); task 4e2
then migrated `service`, `timer`, `daemon_reload` and `systemd_timer` — the
four kinds that funnel through `resource/systemd`'s shared systemctl
`Client` (`NewClient` from a `*runners.SystemdRunners`), plus `service`'s
own `*runners.ServiceRunners` for its BSD backends and manager detection.
Task fg2 migrated `cron` (`*runners.CronRunners`, which also selects the
crontab lock strategy; `FakeCrontab` and `FakeCrontabLock` are gone).
`package` still uses the `internal/testseam` fakes described above until
fg2's package slice lands; not an inconsistency to fix ad-hoc.

A public `api.ApplyWithRunners` briefly existed for this (task qb2's first
slice) so a cross-package test outside `api` (`resource/
dryrun_fitness_test.go`) could inject a fake runner without importing
`internal/testapply`. It was itself an accidental public test seam —
callable from an external module, though its sole real parameter type
(`internal/runners.Set`) lives under `internal/` and so is unnameable
there — and task 3f2 unexported it once `internal/testapply.ApplyWithRunners`
was confirmed to cover the same need; no client module called it.

### Direct repository apply removed after v0.15.0

Removed in v0.16.0 (task e72); no known users (neither client module called
it). `api.Apply`/`api.Run` and the plan engine were already the sole apply
path for recipes; this removes the legacy path they had superseded. Removed:

- `resource.Apply` (the package-level function; use `api.Apply` or
  `api.Run`)
- `resource.Resource.Apply`
- `resource.Multi.Apply`

Tests inside this module that used to call `resource.Apply` now call
`api.Apply` (from `api` or an external test package) or the new
module-internal `internal/testapply.Apply` (from a `resource/<kind>`
package's own tests, which cannot import `api`).

### resource.Applier contract removed (task ub2)

Removed in v0.16.5 (task ub2), a follow-up to the direct-repository-apply
removal above. `resource.Register` used to still require its
registered-value argument to satisfy a `resource.Applier` interface, so every
resource kind kept a now-pointless one-line `Apply` method (e.g.
`file.File.Apply`) and a compile-time assertion, even though nothing called
it any more. That contract is gone: `resource.Register`'s third parameter is
untyped (`any`), `resource.Applier`/`resource.ApplierFunc` no longer exist,
and no kind has an `Apply` method. `resource.Registered` returns the
registered value untyped; its only real consumer,
`resource/systemd/merge.go`, type-asserts it back to
`*DaemonReloadResource`.

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
