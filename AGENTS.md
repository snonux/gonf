# Agent Guidelines

## Resource Management
Resource packages expose (a subset of) the same constructor trio, and the name says
whether the constructor registers:
- `Present` and `Absent` (plus variants such as `file.PresentEnsure`) build
  the resource, register it with `resource.Register` and record its plan draft
  (`resource.RecordPlanDraft`). The caller must not register it again.
  Recording the draft is gated on `Register`'s `ok` (task sf2):
  `r, ok := resource.Register(...); if ok { resource.RecordPlanDraft(...) }`.
  `ok` is false for a duplicate ID, which `Register` reports as a
  declaration error without registering; an ungated `RecordPlanDraft` would
  then overwrite the FIRST declaration's draft under that ID, so the
  repository would apply the refused declaration. The same gate covers
  every other per-ID side effect of a registration (`systemdtimer.Present`'s
  `systemd.JoinRegisteredReload`, `testapply.Register`'s `fixtures` entry)
  and, in a composite that registers several resources for one declaration,
  every member registration: `configset.Present` registers no member when
  the set's own `Register` returned `!ok`.
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
  removed (task kd2), since the reset only ever discarded whatever the
  recipe had registered earlier at top level, with nothing to show for
  it. Removing it does NOT make a same-ID collision impossible on that
  path, though: each `WhenHostname`/`WhenPathExists` call is an
  independent condition, and several can match the same local host at
  once (overlapping substrings, two `List(...)` entries, two existing
  paths), so their bodies run into the SAME repository in turn. Direct
  apply therefore requires resource IDs to stay unique across every
  fragment matching the host — unlike the recording branch, which
  deliberately keeps each fragment in its own scope — and a collision is
  reported through `resource.Register`'s `declerr.Reportf` with both
  colliding `When*`/`WhenPathExists` conditions named
  (`resource.collisionError`, task vd2; an earlier version of this note
  wrongly claimed collisions could never happen here, until a probe
  reproduced one with two `WhenPathExists` fragments).
  This collision refusal stays sticky for the rest of the process the same
  way every other declaration error does (see the previous bullet) — task
  oe2 weighed narrowing that specifically for a When*-fragment collision
  (a later, wholly unrelated `File(q); Apply()` is refused with the SAME
  sticky error and `q` is never written,
  `TestWhenPathExistsDirectCollisionPoisonsLaterUnrelatedApply`) and kept
  it sticky: a collision reports through the exact same
  `resource.Register`/`declerr.Reportf` path as every other declaration
  error, the registered-resource repository is process-wide shared state
  with no notion of "which `Apply` call a registration belongs to" that a
  narrower, per-Apply refusal could hang off, and the only path that can
  produce this collision — direct, non-recording apply — is a single-shot,
  top-level-in-main usage pattern; the recording path
  (`RecordPlanTo`/`Run`), the recommended mechanism for a long-running
  embedding process, gives every `WhenHostname`/`WhenPathExists` fragment
  its own repository scope and so cannot hit this collision at all. What
  WAS missing (and what oe2 added) is a production-safe way out for a
  library embedder that does keep reusing the process after fixing the
  recipe that caused a declaration error: `resource.ResetDeclarationError`
  clears only the sticky `declerr` first-error slot and returns what it
  cleared, in one atomic swap (`internal/declerr.TakeFirst`, tasks tf2/kg2
  — see below), unlike the test-only
  `resource.ResetForTest`, which also wipes the registered repository, its
  drafts, the apply report, dry-run, AND the `declerr` capture sink
  (`internal/declerr.Reset`).
  Two follow-on bugs, both confirmed by probe and fixed together (tasks
  tf2/vf2): (1) `ResetDeclarationError` used to call the same `declerr.Reset`
  `ResetForTest` uses, which clears the capture sink `RecordPlanTo` installs
  for the duration of a recording (`api/plan.go`'s `enterRecordMode`,
  `declerr.Capture(stashBodyError)`) as well as the sticky first error. A
  task body that defensively called `resource.ResetDeclarationError()`
  mid-recording — its own doc comment always framed the supported use as the
  direct-apply path, not mid-recording, but nothing enforced that — silently
  tore down that sink too: a LATER declaration error in the same body (e.g.
  a failed `MustSecret`) then missed the recording's capture and landed on the
  process-wide sticky slot instead — and, at the time, nothing re-checked
  `declerr.First()` after a record completed (`RecordPlanTo` checked it only
  before recording started), so the record finished as if nothing had
  failed, silently writing a credentials file with an empty secret and
  returning a nil error. Fixed by giving `internal/declerr` a narrower
  first-only clear that never touches the sink (now `TakeFirst`, which
  task kg2 made a single locked swap so a report racing the call cannot be
  cleared without being returned); `ResetDeclarationError` is exactly that
  call (mid-recording it returns nil and a later report still fails the
  record), and `resource.ResetForTest`
  calls the broader `declerr.Reset` directly (it still needs the full wipe
  between tests). tf2 closed only THIS route into the sink, though:
  `resource.ResetForTest` still calls the broader `declerr.Reset` by design,
  so a task body that mis-calls `resource.ResetForTest()` (rather than
  `ResetDeclarationError`) mid-recording still tears down the sink the exact
  same way — task hg2 found and closed that second route, structurally
  rather than by patching this one further: `api.RecordPlanTo` now
  re-checks `declerr.First()` once more right after `recordPlanBody`
  returns a nil error, and fails the record with whatever it finds — so a
  lost sink is caught regardless of which reset call (`ResetForTest`,
  `ResetDeclarationError`, or any future code with the same effect) caused
  it, rather than chasing each route one at a time. `ApplyChunksContext`
  still runs no check of its own and does not need one: every path that
  reaches it locally (`api.Run`) only does so after a `RecordPlanTo` call in
  the very same process already returned cleanly — and this same, now
  doubly-checked `declerr.First()` guard — so a plan built from a
  poisoned recording never reaches it in the first place; a plan decoded
  from a file or shipped from a different process carries no relationship
  to this process's `declerr` state at all. (2) Even outside a
  recording, `declerr` carries EVERY declaration-error class through the
  same one sticky slot, and `ResetDeclarationError` could not distinguish
  which class it was clearing: a collided resource ID is safe to clear and
  simply continue from (no OTHER resource's registered value depended on
  the collision), but a failed `MustSecret`/`OptionalSecret`/`ResolveSecret`
  lookup is NOT — those calls cannot return an error, so a resource built
  inline from one (e.g. `WithContent("password="+MustSecret(...))`) is
  already registered, holding the empty zero-value string, by the time the
  failure is reported; clearing and continuing leaves it registered with an
  empty or wrong value that a later `Apply` then happily writes. Fixed by
  making `ResetDeclarationError` return the error it discarded instead of
  discarding it silently, so a caller must look at what it is clearing: for
  the unsafe (secret-resolution) class, the safe remediation is to also call
  `resource.ResetRepository()` right after clearing the error, so every
  resource is re-declared from scratch against a recipe that can now
  resolve the secret correctly, rather than just clearing and continuing
  with the half-registered set. `api.Apply`'s own doc comment carries the
  same warning.
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
`internal/remote.ObserveBootstrapForTest`, `internal/testapply.
ApplyWithRunners`). `resource.ResetDeclarationError` (task oe2) is not one of
these: it is not named `*ForTest` and is a real production API — the
supported way for a library embedder to clear the sticky declaration-error
refusal (see "Registration-time contract") without going through the
test-scoped `resource.ResetForTest`.

**Preferred mechanism for a migrated kind (task qb2): per-apply runner
injection through `plan.ApplyContext.Runners`.** Instead of a process-global
fake, a `*internal/runners.Set` travels down from the context passed to one
apply (`internal/runners.WithSet`/`FromContext`, an unexported context key —
an external recipe module can observe only the nil zero value, i.e. "use the
real runner"), threaded by `plan.ApplyWithContext` into every
`plan.Handler.Apply` call for that run. A migrated concrete resource type
gains unexported runner fields (e.g. `resource/cmd`'s `Cmd.runFn`/`probeFn`)
that a `newXWith` constructor sets from an injected `*runners.Set`, mirroring
`resource/user`'s `newUserWith` (task 372); the exported `Ensure` funnels
through an unexported `ensureWith` both `newXWith` and the plan handler call.
The plan handler reads `ctx.Runners` (nil-safe field helpers such as
`runners.CommandOf`) and builds the resource with it. Two narrow
module-internal hooks carry a `*runners.Set` into one whole apply for tests
that cannot build `plan.ApplyContext` themselves: `internal/testapply.
ApplyWithRunners` (any `resource/<kind>` package's own tests, or a
cross-package, non-`api` test such as `resource/dryrun_fitness_test.go`) and,
inside `api` itself, its unexported `applyWithRunners` (reached only by
`api.Apply` and `api`'s own tests). `api/login_class_test.go` shows the third
form: a test able to build plan ops directly calls `plan.ApplyWithContext` +
`runners.WithSet` itself, with no hook at all. `api.ApplyWithRunners` was
briefly exported for this (task qb2's first slice) so `resource/
dryrun_fitness_test.go` could inject cmd's fake runner without importing
`internal/testapply`; that made it a real, callable public API of an
`internal/runners.Set` parameter type an external module can never name —
itself the accidental public test seam this section forbids. Task 3f2
unexported it (`internal/testapply.ApplyWithRunners` already covered every
caller outside `api`) rather than adding it to the allowed-seam list above.

`resource/cmd` (the `command` plan kind, task qb2), as of task 4e2 the
four kinds that funnel through `resource/systemd`'s shared systemctl client —
`service`, `timer`, `daemon_reload` and `systemd_timer` — and, as of task
fg2, `cron` have migrated to this mechanism. `package` remains on the older
`internal/testseam` mechanism below until task fg2's package slice lands:
a known, in-progress migration, not an inconsistency to fix ad-hoc.

`cron` folds its lock strategy into the same injection instead of keeping
a global lock seam: a non-nil `*runners.CronRunners` means the crontab is
faked, so the transaction takes an in-process lock (a faked crontab is not
shared with other processes, and the real flock would create state in the
test user's home directory); `CrossProcessLock` keeps the real lock, which
`resource/cron`'s own lock tests use with a private lock directory.
`cron.EnsureWith` is exported, like `timer.EnsureWith`, because `api`'s
option-fitness test compares a direct apply with a plan round trip. `internal/runners.Set` gains
one field per kind (or, for the systemd-4 slice, one shared field) as it
migrates (see that type's own doc comment).

The systemd-4 slice shares ONE injection point instead of four: every
systemctl call in the module (`resource/systemd.Client`'s `IsActive`,
`IsEnabled`, `Run` and `Command` methods) funnels through a
`resource/systemd.Client` value (its zero value: the real `internal/exec`
runner), built with `NewClient(sr)` from a `*runners.SystemdRunners`
(`internal/runners.Set.Systemd`). The package has no package-level
`IsActive`/`IsEnabled`/`Run` shortcuts (task pg2 removed them, since they
hard-coded `Client{}` and so silently bypassed the injection); a caller that
really wants the real runner, such as a live test, writes `Client{}`
explicitly (`TestNoPackageLevelClientBypass` pins this). Service's
systemd backend, Timer and DaemonReload each hold or build a `Client` from
their own `newXWith`/`newReloadWith` constructor; DaemonReload and Timer
additionally export `EnsureWith(sr *runners.SystemdRunners, ...)` (unlike
`resource/cmd`'s unexported `ensureWith`) because, like `resource/dir`'s
composed `file.EnsureWithPlanFacts`, another resource package composes them
— `resource/systemdtimer` threads its own injected `*runners.SystemdRunners`
into both, and into its own exported `EnsureWith`. `resource/service` also
carries a second, service-specific override, `*runners.ServiceRunners`
(`Run` for the BSD backends' command runner, `Manager` for
`detectServiceManager`), consulted by `Service.run()`/`detectManager()`
instead of a process-global fake; its own `EnsureWith` takes both.

A backend's host-command runner or host detector, for a kind not yet
migrated to `internal/runners` (`package`), is an unexported
function that consults the module-internal `internal/testseam` fake first
and otherwise calls the real `internal/exec` runner or detector. Tests
anywhere in the module install fakes with `testseam.Fake*(t, ...)`; each
fake is one layer removed by `t`'s cleanup. In-package tests may instead
hand a backend its runner directly (as `applyWith` in pkg, or `newUserWith`
in resource/user). Log capture is `internal/testutil.CaptureLog`. A newly
migrated kind follows the `internal/runners` pattern above instead of adding
another `testseam` fake.

The fakes and the log capture are process-global, so a test using them must
not run in parallel. They enforce it: each calls `t.Setenv`
(`testseam.ParallelGuardEnv`), so testing panics when that test or one of
its ancestors calls `t.Parallel`.
