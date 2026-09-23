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
dependency ordering; it (and `api.Run`) is the only apply path. The direct
`resource.Apply()` repository path (with `Resource.Apply` and `Multi.Apply`)
was retired in task e72: the repository only records registrations, their
dependency edges and their plan drafts. Tests apply registered resources like
this:
- from `api` or an external test package: `api.Apply` (or `api.Run`);
- from a `resource/<kind>` package's own tests (which cannot import `api`):
  `internal/testapply.Apply`, which lowers the drafts with the same plan
  handlers and applies them through `plan.Apply` after the same
  `plan.ValidateChunks` pre-flight (`api`'s `TestTestapplyOpsMatchApply`
  pins that it lowers the same ops). A stand-in resource that only notes a
  status (e.g. a watched `File[unit]` reported changed) is
  `testapply.Register(type, name, testapply.Noting(status, ids...))`,
  never a bare `resource.Register` (it has no draft, so the apply refuses it);
- behaviour the plan pre-flight makes unreachable (e.g. a change gate
  watching an ID nothing notes) is tested on the direct `Ensure` path.

`resource.Register`'s third parameter is the registered value untyped
(`any`, task ub2 dropped the vestigial `resource.Applier` contract it used to
require): a kind passes its own concrete value (e.g. the `*file.File` it
built), with no `Apply() error` method or compile-time assertion needed any
more. `resource.Registered` hands that value back untyped; the only real
consumer is `resource/systemd/merge.go`, which type-asserts it back to
`*DaemonReloadResource` to fold a later same-bus declaration into the one
already registered in this recipe scope.

## Shared embeds
State common to all concrete resource types lives in the `embed` package and is
embedded rather than redeclared:
- `embed.DependsOn` — dependency IDs (`IDs` field) plus the `AddDependency` method.
- `embed.Absence` — the `Absent` field plus the `SetAbsent` method (implements
  `opt.Absentable`).
- `embed.Sensitivity` — the `Sensitive` field plus the `SetSensitive` method
  (implements `opt.Sensitivable`, the `WithSensitive` option), embedded only
  by the payload-carrying kinds (File, Dir, ConfigSet, Command, Package, Cron,
  SystemdTimer; a ConfigSet member implements `SetSensitive` itself and marks
  its set). The resource copies it to `PlanDraft.Sensitive`, which
  `api`'s `draftToOp` ORs into `plan.Op.Sensitive`; its plan handler passes
  `WithSensitive` back for a sensitive op, and its apply reads the field to
  withhold secret-bearing details. The option family is
  `opt.SensitiveOption`; a new payload kind adds its marker there.
- `embed.Misuse` — collects option misuse (`ReportMisuse`, implements
  `opt.MisuseReporter`; `MisuseErr`). Every concrete resource type embeds it
  and checks `MisuseErr()` right after applying its options: `Present`
  reports it through `resource.Refuse`, `Ensure*` returns it (see
  "Registration-time contract").
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

## Registration-time contract
Library code never ends the process: there is no `logger.Fatal`, and no
`os.Exit` or `panic` for recipe or input errors (only `cmd/gonf` exits, with
`cli.CLI`'s code). DSL misuse detected while a recipe declares its tasks,
inventory and resources is a declaration error (`internal/declerr`):
- Report it with `declerr.Report` / `declerr.Reportf` (a resource package
  uses `resource.Refuse(type, name, err)`, which reports and returns the
  unregistered value) and return an inert value — an unregistered resource,
  an empty `resource.Multi`, a zero handle, a nil list — so the recipe keeps
  running and later declarations are still checked. Never register a refused
  declaration. `Reportf` wraps its cause with `%w`, not `%v` (`declerr.Error`
  implements `Unwrap` precisely so `errors.Is`/`errors.As` see through a
  report); a new call site follows the same `%w` pattern instead of copying
  an older `%v` one.
- An option reports misuse to its target (`misuse` in resource/options):
  a target with `embed.Misuse` collects it, anything else goes to declerr.
- The first report wins and carries the recipe line (`declerr.Location`).
  While `RecordPlanTo` records, reports are captured into the session and
  fail that record; outside a recording the first one is kept for the
  process, and `RecordPlanTo`, `Run`, `api.Apply` and `cli.CLI` refuse with
  it (the CLI prints it and exits 1). `RecordPlanTo` fails a record for
  other reasons too, never reported to declerr: a task recursion cycle, a
  packaging error, a panicked task body (recovered and re-panicked, tasks
  cd2/jd2). On every outcome — success, failure or a recovered panic —
  `RecordPlanTo` restores the registered resource repository to its exact
  snapshot from before the call started (`resource.SnapshotRepository`,
  tasks ad2/bd2/id2), so nothing THIS call itself registered can outlive
  it (a failed body's partial registrations, or a successful record's
  last scope — e.g. a `WhenHostname`-guarded fragment whose ops correctly
  carry `when_begin`/`when_end`, but which a later, unguarded `api.Apply`
  would otherwise lower and apply directly), AND whatever was registered
  before the call genuinely survives it — an earlier, ID-based
  `RollbackTo(kept []string)` only pruned down to a set of names, so a
  same-ID registration the record attempt itself made was kept with its
  OWN value instead of the original being restored (task id2); swapping
  the whole repository pointer back cannot have that failure mode.
  `WhenHostname`/`WhenPathExists`'s own non-recording branches (direct,
  not through `RecordPlanTo`) used to reset the repository before running
  a matched branch's body too, with nothing to restore it afterward —
  removed (task kd2), since only one branch's body ever runs directly at
  all, so there was never a same-ID collision to guard against there.
  `RecordPlanTo` also sets `api`'s own `lastRecordFailure` on ANY failure
  including a recovered panic (not `declerr`'s concern, see api/plan.go)
  — cleared by a later clean record. `api.Apply` refuses on it
  UNCONDITIONALLY (task ad2 reverted an earlier, narrower empty-repository
  scoping from task tc2, once it found that scoping let a later, unrelated
  registration mask an earlier one's silent loss; kept unconditional even
  after id2 closed that specific loss, as defense in depth against
  whatever failure shape id2's fix does not happen to cover): the ONLY way
  to clear a failed record's refusal is a later record that actually
  succeeds, never merely registering or applying something new directly.
  A test that intentionally fails a record and continues in the same
  process must therefore call `api.ResetForTest` (or record cleanly
  again) rather than relying on a fresh registration alone; several tests
  in `api/*_test.go` do this via
  `t.Cleanup(func() { lastRecordFailure = nil })` specifically, where the
  full `ResetForTest` would reset more than the test wants.
- Code below the DSL (internal packages such as `internal/inventory`, check
  helpers) returns errors; only the DSL entry point reports them.
- Keep a `panic` only for a genuine, documented programmer-bug invariant that
  no recipe or input can reach (the list is in docs/plan.md, "Error handling
  contract").
- Test misuse in-process: reset, declare, then assert `declerr.First()` (or
  the `RecordPlan` error for misuse inside a task body); no helper
  processes. `api` tests use `requireDeclErr`.

## Test seams
Public (non-`internal`) packages export no `*ForTest` setters; the state
resets `api.ResetForTest`, `resource.ResetForTest` and `plan.ResetForTest`
are the one exception. Module-internal packages may keep a narrow test hook
that clients cannot import (`internal/clihost.SetForTest`,
`internal/remote.ObserveBootstrapForTest`).

A backend's host-command runner or host detector is an unexported function
that consults the module-internal `internal/testseam` fake first and
otherwise calls the real `internal/exec` runner or detector (e.g.
`resource/systemd`'s `runCmd`). Tests anywhere in the module install fakes
with `testseam.Fake*(t, ...)`; each fake is one layer removed by `t`'s
cleanup. In-package tests may instead hand a backend its runner directly (as
`applyWith` in pkg and service, or `newUserWith` in resource/user). Log
capture is `internal/testutil.CaptureLog`. A new backend runner follows the
same pattern.

The fakes and the log capture are process-global, so a test using them must
not run in parallel. They enforce it: each calls `t.Setenv`
(`testseam.ParallelGuardEnv`), so testing panics when that test or one of
its ancestors calls `t.Parallel`.
