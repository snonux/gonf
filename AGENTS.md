# Agent Guidelines

## Resource Management
Resource packages expose (a subset of) the same constructor trio, and the name says
whether the constructor registers:
- `Present` and `Absent` (plus variants such as `file.PresentEnsure`) build
  the resource, register it with `resource.Register` and record its plan draft
  (`resource.RecordPlanDraft`). The caller must not register it again.
- `Ensure` (plus variants such as `file.EnsurePresent` and
  `file.EnsureWithPlanFacts`) builds and applies the resource immediately
  WITHOUT registering it or recording a draft. It exists for composition: plan
  handlers and composite resources (e.g. `dir` writing individual files)
  converge a member that must not become its own top-level resource.
- Exception to the `Ensure*` naming: `dir.EnsurePlanDraft` neither registers
  nor applies. It only builds the `ensure_dir` draft and returns it, so its
  caller (`api.EnsureDir`) records it.

The public `api` wrappers (`api.File`, `api.Dir`, `api.EnsureFile`, ...) are
thin and delegate to `Present`/`Absent`; composite wrappers (`api.LoginClass`,
`api.SystemdUnits`, `api.GitGlobal`, ...) create several resources. Either
way, each resource they create is registered exactly once, by its resource
package; the wrappers never call `resource.Register` themselves. The
exceptions are the conditional recipes, which may return an empty `Multi`
(safe to pass to `DependsOn`) and register nothing:
- `api.EnsureDir` and `api.LinkIfExists` in plan-record mode record a recipe
  draft directly (`resource.RecordPlanDraft`) instead of registering, because
  the decision is made on the destination host.
- `api.EnsureDir` in direct (non-recording) mode registers nothing when the
  path already is a directory on the controller (a symlink to a directory
  counts); otherwise it delegates to `api.Dir`. `api.LinkIfExists` in direct
  mode always delegates (to `api.Link` or `api.NoLink`).
- `api.SecretFile` delegates to the registering `file.PresentSecret` once its
  secret resolved; when the resolution fails it registers nothing and returns
  an empty `Multi`, because the stashed secret error already fails the record
  (like `MustSecret`).

A new registering constructor follows the same rule: it registers what it
creates, and a non-registering helper is named `Ensure*`.

## Dependencies
Concrete resource types (e.g. `file.File`, `dir.Dir`, `link.Link`, `pkg.Package`)
embed `embed.DependsOn` to accumulate dependency IDs. The `DependsOn` option
records those IDs, and each `Present` function forwards them into
`resource.Register(..., x.DependsOn.IDs...)`. A `Multi` dependency is expanded
via `Dependencies()` so each member is depended upon individually.

The public `api.Apply()` snapshots registered drafts and uses the plan engine's
dependency ordering. The lower-level `resource.Apply()` repository path is a
legacy direct path kept for tests and compatibility; new code uses `api.Apply`
or `api.Run`, as its doc comment says ("Prefer api.Apply or api.Run"). It is
deliberately not marked `// Deprecated:` yet: its many in-repo test callers
would then fail `go tool staticcheck` (SA1019). Add the marker only together
with migrating or retiring those callers.

## Shared embeds
State common to all concrete resource types lives in the `embed` package and is
embedded rather than redeclared:
- `embed.DependsOn` — dependency IDs (`IDs` field) plus the `AddDependency` method.
- `embed.Absence` — the `Absent` field plus the `SetAbsent` method (implements
  `opt.Absentable`).
- `embed.ChangeGate` — the change gate (`Gated`, `Watch`) and its behaviour.
  It holds the one watch list of the change-gate option family: `OnChange`,
  `WatchChanges` and the legacy daemon-reload `IfChanged` (arm only) all
  call `SetChangeWatch` (implements `opt.ChangeWatchable`). The legacy
  `WithWatch(ids...)` keeps its old semantics (sets, does not arm, a later
  call replaces earlier ids) through `opt.Watchable` (`SetWatch`), a slot on
  daemon-reload only that `newReload` appends after the gate ids.
  `IfChanged` requires `opt.ChangeGated` (`ChangeWatchable` + `Watchable`),
  so both legacy spellings stay rejected on Service, Timer and Command even
  through the type-erased `Option` path; the embed must therefore not
  implement `SetWatch`. The embed owns watch de-duplication (callers never
  dedupe), `AddWatch` (add ids without arming; daemon-reload only, for its
  `WithWatch` ids, its `DependsOn` fallback and merging), the
  nothing-to-watch check (`CheckWatch`), the hold predicate
  (`Holds(resource.AnyChanged)`), the held-action debug log (`LogHeld`) and
  the plan-draft wiring (`DraftGate`). Gated resources call these instead of
  re-implementing the checks; `resource.NoteIdle` reports a gate-held
  action as skipped. Plan handlers rebuild a recorded gate with
  `opt.RecordedChangeGate`. Daemon-reload is the one exception: it resolves
  its watch list once after its options ran (gate ids, then `WithWatch`
  ids, else its `DependsOn` ids; only `Ensure` then runs `CheckWatch`, since
  a registered bare `IfChanged` may still merge with a same-bus declaration
  and the plan pre-flight refuses one that stays unwatchable), it keeps
  its own draft wiring (`Watch` is recorded even when unarmed) instead of
  `DraftGate`, and it holds with `Holds(d.changedSinceLastReload)` instead
  of `resource.AnyChanged`: only a watched change noted after the bus's
  last reload in this apply (`resource.ChangedSince` on its
  `DaemonReload[bus]` ID) fires it, so a SystemdTimer that joined the bus's
  registered reload (`systemd.JoinRegisteredReload`) shares one reload
  with it.

When adding a field or capability shared by every resource type, prefer a new
embed type here instead of duplicating the field and its setter in each resource.
