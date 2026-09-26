# Changelog

Release notes for gonf. Each release is also an annotated `v*` tag; for
releases before v0.17.0 the tag message and the git log are the notes.

## Unreleased

More recipe DSL sugar. No plan schema change: every new form records
exactly the plan of the long form it replaces. Nothing breaks in recipes.

Tasks ([docs/reference.md](docs/reference.md), "RegisterMethods", "Needs")
- `Needs(Unattended.Script)` takes method expressions (and method values)
  besides names, resolved to the task `RegisterMethods` registered for the
  method, so an editor can jump to and rename a need.
- `gonf-desc` (`//go:generate go run github.com/snonux/gonf/cmd/gonf-desc`)
  writes the `DescX` companions from the task methods' doc comments.
- `RegisterMethods` takes a `TaskOption` directly, as `WithGroupWhen` does.
- `RegisterOnCluster(cluster, structs...)` registers several structs on
  one cluster, each under its default prefix.
- `WhenHostnameIn(hosts...)` guards a task to part of its cluster.
- `AggregatePrefix("x")` is the `^x_` pattern aggregate.
- `DefaultPrefix` keeps `WireGuard` one word (`wireguard_`).

Resources ([docs/reference.md](docs/reference.md), "Shared options", "File", "Link")
- `RootOwned`, `RootExec`, `RootPrivate`: `Perm(..., Root)` with the usual
  file and directory modes.
- `WithContentFrom(render(...))`: a render error refuses the file.
- `WithShellVar("vm_enable", "YES")` owns an rc.conf-style `key="value"` line.
- `Symlink(path, target)` is `Link(path, WithSymlink(target))`.

Inventory and hosts ([docs/reference.md](docs/reference.md), "Inventory", "Per-host fragments")
- `WithHostnameMatch(f)` sets the hostname fragment a host's cluster
  guards match on (default: the inventory name).
- `EachHostWith[T]` skips members without a `T` value.

CLI
- `cli.Main()` is `os.Exit(cli.CLI())`.

Fixes
- An `OnChange` (or `SystemdUnits` `FanIn`) watching a `DestHome` resource
  now fires when that resource changed. Before, the gate compared the
  recorded `File[${HOME}/...]` id with the expanded path the apply noted,
  and never fired.

Docs
- [docs/tutorial/](docs/tutorial/README.md): a step-by-step tutorial in 15
  chapters, with runnable example recipes and captured output.

## v0.23.0 (2026-09-25)

Plan schema 27, declared only by a plan that uses it.

Files ([docs/reference.md](docs/reference.md), "File")
- `WithBlock(name, lines...)` owns a marked region of a shared file: the
  lines between `# BEGIN GONF <name>` and `# END GONF <name>` are replaced,
  every other line is left alone, and a file without the markers gets the
  block appended. Malformed markers fail the apply without writing;
  overlapping ownership with `WithLine`/`WithoutLine`/`WithKeyedLine` fails
  at declaration. The block travels as the file op's `blocks` field; an
  older destination refuses the v27 header instead of skipping the block.

## v0.22.0 (2026-09-25)

Recipe DSL simplifications. No plan schema change. Breaking for recipes in
three places, marked below.

Tasks ([docs/reference.md](docs/reference.md), "RegisterMethods")
- **Breaking:** an `OptsX` companion now adds to the struct defaults
  (`RequiresRoot`, `Opts()`) instead of replacing them, so an `OptsX` that
  only adds `Needs` keeps `Privileged()`. `Unprivileged()` is the new
  opt-out; an empty `TaskOptions` no longer opts out.
- **Breaking:** without `WithPrefix`, `RegisterMethods` prefixes task names
  with the struct's package and type name (`DefaultPrefix`): `home.HomeTasks`
  registers `home_*`, `freebsd.Unattended` registers `freebsd_unattended_*`.
  `WithPrefix("")` keeps bare method names.
- `WhenX() TaskOption` companions (e.g. `return WhenLinux()`) record a
  serializable guard, so the task still pushes. `WhenX(Facts) bool` keeps
  working as the opaque, controller-only form.

Imports
- **Breaking for a dot import of both:** `api` now re-exports every
  resource option, the inventory, `Refuse` and `Dependency`, so
  `import . "github.com/snonux/gonf/api"` is the only import a recipe
  needs. Drop the dot import of `api/options`; Go rejects the duplicate
  names. `api/options` stays for qualified use.
- New package `inventory` holds `Host`, `Cluster`, `Fleet`, the host options
  and lookups, with its own godoc page; `api` re-exports all of it.

Inventory ([docs/reference.md](docs/reference.md), "Inventory")
- `WithSSHDomain(d)`: the SSH hostname defaults to `<name>.<d>`.
- `WithPlatform("goos/goarch")` sets `WithGOOS` and `WithGOARCH` at once.

## v0.21.1 (2026-09-25)

Bug fix, no plan schema change.

Cron ([docs/design/cron.md](docs/design/cron.md), "Adopting existing entries")
- An unmanaged entry identical to a present job is now adopted even when a
  `NAME=value` line follows it (another job's `WithCronEnv` block, say). The
  job's block replaces the entry in place, so it keeps the environment it
  ran with. v0.20.0 and v0.21.0 left such an entry alone and appended the
  block, so the job ran twice.
- A crontab left in that state is repaired on the next apply: an identical
  unmanaged entry beside the job's existing block is removed.

## v0.21.0 (2026-09-25)

macOS destinations. Plan schema 26, declared only by a `ConfigSet` with a
`${HOME}` path; every other plan keeps its older header and encodes
exactly as before.

Paths ([docs/reference.md](docs/reference.md), "Other helpers")
- `DestHome(elem...)` returns `${HOME}/elem...`, expanded on the
  destination at apply. `Home` = where the recipe runs (sources), `DestHome`
  = where the plan applies (targets). `Home` records the controller's home
  literally, so a push from Linux to a Mac wrote into `/home/paul` there.
- `${HOME}` already expanded in op paths, link targets, `link_if_exists`,
  `Command` `WithDir`/`Creates` and `WhenPathExists`. It now also expands in
  `ConfigSet` member paths, `WithChroot` and `WithStagingDir` (the schema 26
  bit), and in the direct-mode probes of `EnsureDir`, `LinkIfExists` and
  `WhenPathExists`.
- `${HOME}` is the applying process's `$HOME`, falling back to its user
  database entry; an empty or relative home fails the apply. Under sudo/doas
  it is whatever home the elevation tool leaves.
- The token in a controller-side source (`WithSource`, `WithSourceGlob`,
  `WithSourceBase`, `InstallFile`/`SyncDir` sources) or in `WithHome` is a
  declaration error.

Facts and guards ([docs/reference.md](docs/reference.md), "Task options", "Facts")
- Profile `darwin` on macOS (was `unknown`). Controller, destination and
  template facts share one detection; Linux and BSD results are unchanged.
- `WhenDarwin()`, `WhenFreeBSD()`, `WhenOpenBSD()`, `WhenNetBSD()`,
  `WhenBSD()` and `WhenOS(goos...)` task guards, destination-evaluated like
  `WhenLinux()` (several names record one `in` predicate). An unknown GOOS
  is a declaration error. No body-level GOOS helper yet: use a task guard.

## v0.20.0 (2026-09-25)

Plan schema 25, declared only by a plan that uses `WithFlags` or `Noop`;
every other plan keeps its older header and encodes exactly as before. DSL
ergonomics: existing recipes record the same plans (the one behaviour
change is Cron's identical-entry adoption, below).

Ownership ([docs/reference.md](docs/reference.md), "Shared options")
- `Perm(mode, owner)` sets mode and ownership in one option wherever
  `WithMode`/`WithOwner`/`WithGroup` are accepted: `Perm(0o640, "root:wheel")`.
  `owner` is `"user:group"`, `"user"` or `":group"`; a malformed one is a
  declaration error. The recorded plan is byte-identical to the three-option
  form.
- `Root` is the owner "root plus the destination's root group" (root on
  Linux, wheel on the BSDs and macOS): `Perm(0o755, Root)` or
  `WithOwner(Root)`. It records group `0`, which every destination already
  applies, so older destination binaries apply such plans unchanged.
- `WithOwner` accepts the same `"user:group"` spec. A plain user name records
  exactly as before; an owner with a colon used to fail at apply time.
- Parent-directory ordering: a resource creating something inside a
  directory the same plan creates (`Dir`, `SyncDir`, `EnsureDir`) applies
  after it without `DependsOn`. The edges are inferred from the paths at
  apply time, so plan files are byte-identical; conf and dotfiles apply in
  the same order as before. See [docs/reference.md](docs/reference.md#shared-options).

Tasks ([docs/reference.md](docs/reference.md), "Tasks")
- `OnCluster(name)` RegisterOption: `WithCluster` plus a task-level
  destination guard (hostname contains one of the cluster's hosts, the
  match `WhenHostname(ClusterHosts(), ...)` uses), so bodies drop that
  wrapper. Recorded as one `when_begin` per task, not one fragment per host;
  `-list` marks it destination-guarded off-cluster. An unknown cluster is a
  declaration error.
- `Needs(tasks...)` TaskOption: needed tasks record right before the task,
  once per `Run` list or aggregate tree, each with its own guards. Names
  resolve relative to the `RegisterMethods` prefix first, then as full
  names. A cycle is a declaration error; an unknown need fails the record.

Inventory ([docs/reference.md](docs/reference.md), "Inventory")
- `HostDefaults(opts...)` bundles host options; later options override
  earlier ones, including a bundle's `WithValue` keys and `WithData` types.
- Typed host data: `WithData(v)` stores a value by its concrete type;
  `EachHost[T](func(T))`, `EachHostNamed[T](func(host, T))` and
  `HostData[T](host)` read it with `ForHosts`' and `MustHostValue`'s
  semantics (a member without a value is an error).

Resource keywords ([docs/reference.md](docs/reference.md), approved by the
user 2026-09-24)
- `CronAt(name, "10 6 * * *", command, opts...)` and `WithSchedule("10 6 * *
  *")` spell a cron schedule in one string. Both record the same plan op as
  the per-field options, which keep working. A schedule that is not five
  portable fields (`@reboot` included) is a declaration error.
- Behaviour change: a present `Cron` now adopts an unmanaged entry that is
  identical to it (same five fields and exact command, in its own user's
  crontab) instead of leaving it to run twice. It never adopts for a job
  with `WithCronEnv`, or an entry followed by a `NAME=value` line
  ([docs/design/cron.md](docs/design/cron.md), "Adopting existing
  entries"). `WithLegacyCommand` stays for a different command or schedule;
  passing it with the job's own command records and does exactly what it
  did.
- `Sh("systemctl restart foo", opts...)` is `Command` with a shell-words
  argv: quotes and backslashes work, no shell runs, nothing expands, and
  unquoted shell syntax is a declaration error. Same plan and default ID as
  the `Command` it spells.
- `Noop(name)` registers `Noop[name]`, which runs nothing and reports ok (new
  `noop` plan kind), replacing `Command("true", nil, Unless("true", nil),
  WithName(name))`.
- `Service(name, WithFlags(flags))` manages BSD rc startup flags (rcctl,
  sysrc, NetBSD `/etc/rc.conf`). A flags change fires `WithRestart` even
  behind `OnChange`; OpenBSD `WithFlags("")` matches the `NAME_flags=` line
  of the `File(..., WithLine("httpd_flags="))` pattern it replaces. Refused
  on systemd at apply ([docs/design/service.md](docs/design/service.md)).
- `Packages("git", "tmux")` now takes names (it took a `[]string` plus
  options and had no callers outside `Package`); options go through
  `Package(List(...), opts...)`.

## v0.19.0 (2026-09-24)

Plan schema 24 (unchanged).

- `gonf plan-signer-keygen -h` prints its usage line (it printed an empty
  flag list), plus a documentation pass over every feature since v0.16.6.

Tasks and aggregates ([docs/design/tasks.md](docs/design/tasks.md), "Destination guards")
- Approved behaviour change (8h2, approved by the user 2026-09-24):
  serializable task guards (`WhenLinux`, `WhenProfile`,
  `WhenHostnameContains`, also through `WithGroupWhen`) are evaluated on the
  destination, not the controller. A pattern `Aggregate` or `AggregateTasks`
  records such a member inside its `when_begin` on every controller, so
  `gonf push paul@rocky home` from earth now includes dotfiles'
  `home_tmux_rocky` (`WhenHostnameContains("rocky")`), which it used to drop
  silently. Only opaque predicates (`When(func)`, `WhenFoo` companions) hide
  a task on the controller; a mixed task's opaque part filters there and its
  serializable part travels. Aliases and nested aggregates behave the same.
- `-list` shows such tasks with a `[destination-guarded: <guard>]` suffix
  instead of hiding them (`TaskInfo.DestinationGuard`); other rows are
  unchanged.
- A local run (`gonf home`, `Run`) is unchanged: it resolves member guards
  against this host at record time, so its plan, summaries and outcome are
  identical. `gonf plan` and push/cluster/fleet plans grow by the formerly
  dropped members, each wrapped in `when_begin`/`when_end`.
- `WhenProfile()` without profiles is now an opaque never-true predicate:
  naming such a task fails the record instead of applying it unguarded.

## v0.18.0

Plan schema 24 (unchanged); `-sealed-version` 1, `-signed-version` 1.
The v0.18.0 tag message carries no notes; this entry was added afterwards.

Signed plans ([docs/design/plan-signing.md](docs/design/plan-signing.md))
- Signing phase 2 (7g2): `gonf plan -seal -sign <signer-file>` wraps each
  sealed artifact (every host's with `-for`) in a `GONF-SIGNED-PLAN/1`
  envelope with a signed `signed-at` time; `gonf plan-signer-keygen <file>`
  creates a `0600` signer key and prints its trusted-signers line. The
  "wrote" report names the signer and its `sha256:` fingerprint. An
  envelope from v0.17.0's library-only `Sign` (no `signed-at`) never
  verifies.
- Signing phase 3 (8g2): `gonf apply` verifies a signed plan before
  decrypting it, always: `-trusted-signers` (non-root default
  `~/.config/gonf/trusted-signers`; root must pass it), `-require-signed`
  (refuse any unsigned input), `-max-signed-age` (default 24h, plus a fixed
  5-minute future skew). `gonf plan-verify` runs the same checks and writes
  the bare `plan.age` for the `age -d` emergency path. `gonf
  -signed-version` prints 1. A destination older than v0.18.0 refuses a
  signed plan.
- Signing phases 4-6 (anti-replay counter, threshold signers, an unattended
  entry point) were declined; unattended sealed apply stays blocked.

Sealed plans ([docs/design/plan-encryption.md](docs/design/plan-encryption.md))
- Behaviour change (5b2): `gonf plan -o dir` writes a plan carrying secret
  material as a sealed `dir/plan.age` by default when an operator
  recipients file exists (`~/.config/gonf/recipients` or
  `-recipients-file`); `-plaintext` opts out; a recipients file that exists
  but is unusable refuses such a plan instead of writing plaintext. Plans
  without secret material, and setups without a recipients file, are
  unchanged.

Docs
- conf-rex-gaps.md records conf's finished Rex retirement; the consumer DSL
  plan's status notes are refreshed (662).

## v0.17.0

Plan schema 24 (unchanged). Highlights since v0.16.6 (from the tag):

Sealed plans
- Phase 4 complete (yf2, zf2, 0g2): sensitive sticky-dir blobs read by
  elevated chunks of a multi-chunk push are sealed to a fresh per-push
  ephemeral age1pq key sent only on the chunk's stdin (GONF-PUSH/2) and
  decrypted into a private run dir on the destination. Requires a remote
  gonf >= 0.17.0; older remotes are refused before any upload.
- Plan signing phase 1 (6g2): plan/seal Sign/Verify, an Ed25519 envelope
  over plan.age; hardened LoadSigner/LoadTrustedSigners; weak and
  small-order trusted keys refused. No CLI wiring yet.
- -for bounds peak memory and stages artifacts before a single commit
  (qg2); recipients are printed as fingerprints, full keys with -verbose
  (4g2).

Fixes
- Multi-chunk pushes with blobs: every -apply-dir session wiped the sticky
  dir, so chunks after the upload lost their blobs (regression since
  v0.12.2, fixed in 0g2).
- NetBSD hosts: push resolves GOARCH from uname -p when uname -m names a
  port such as evbarm (found natively on the Raspberry Pis).
- Secret redaction: FlushPoint leak-free forced-flush cut for nested
  forms (sg2, tg2) with a single-pass KMP scan (0h2); RedactingWriter
  keeps one redactor per lifetime (5g2); the relay keeps its tail pending
  across the orphan deadline (zg2).
- Encode is kind-strict like decode (cg2); decode can no longer panic on
  field drift (dg2).
- CLI settings (dry-run, log level, privilege, profile, cmd timeout) are
  scoped to each invocation (vg2, xg2).
- ResetDeclarationError is an atomic take (kg2).

Internal
- All seven runner kinds inject through plan.ApplyContext (fg2); the
  internal/testseam fakes are gone.
