# Changelog

Release notes for gonf. Each release is also an annotated `v*` tag; for
releases before v0.17.0 the tag message and the git log are the notes.

## Unreleased

Fixes
- NetBSD `WithFlags` now writes `/etc/rc.conf` durably (fsync before the
  rename and of `/etc` after it), like a managed `File`, so a power loss
  can no longer leave an empty rc.conf. It also keeps rc.conf's owner,
  edits the file a symlinked rc.conf points to instead of replacing the
  link, and refuses a dangling symlink or a non-regular rc.conf.
- A changed `File` with `WithOwner`/`WithGroup` gets its owner and group
  on the temporary file before the rename, so a set-id file never
  appears, even briefly, owned by the user running gonf (e.g. root). An
  owner or group that does not resolve now fails before the content is
  replaced instead of after.
- `-quiet` and `-verbose` now reach the remote `gonf apply` of `push`,
  `cluster` and `fleet`, and the local sudo/doas re-exec of privileged
  chunks. Before, those children logged at their default level, so
  `-quiet` still printed every remote or privileged change. A
  fixed-argument sudoers/doas rule must now allow these flags too.
- The SSH hostname no longer depends on host option order: it is derived
  once every host option ran. An explicit `WithSSHHost` now wins even when
  it comes before a `HostDefaults` bundle with `WithSSHDomain` (or equals
  the inventory name), and a later `WithSSHDomain` replaces a bundle's
  instead of being ignored, so push connects to the intended host.
- A `WithSSHDomain` option shared by concurrent `Host` calls no longer
  data-races.
- `WithGOOS` now validates its GOOS like `WithPlatform` and `WhenOS` do:
  a name gonf does not manage or a wrongly cased one (`"Linux"`, with a
  lower-case suggestion) is a declaration error instead of a cross-compile
  failure at push time. `WithGOOS("")` still resets to probing `uname -s`.
  `WithGOARCH` and `WithPlatform` likewise refuse a GOARCH that is not
  lower case (`"AMD64"`). All of them, and push's `uname -s` probe, now
  share one list of supported operating systems.
- `Needs(T.Method)` now resolves for the methods of a generic struct
  (`Needs(G[int].Base)`). Before, the need never matched the registered
  task, so the record failed with "needs unknown task". Inside a generic
  method, a method expression or method value over the type parameter
  (`G[X].Base`, `g.Base`) compiles to a closure gonf cannot match to a
  method: name a concrete instantiation or the task there.
- `Needs(T.Method)` now resolves for a struct from a package whose last
  path element contains a dot (`gopkg.in/yaml.v3` style). The runtime
  writes that dot as `%2e` in method names, so the need never matched.
- Behaviour change: a func passed to `Needs` that does not name an
  exported method of a struct (a package function, an unexported method,
  a function literal, or the closure above) is now a declaration error
  when `Needs` is called. Before, it failed only a record that reached
  the task, with "needs unknown task".
- `gonf-desc` refuses a documented task method whose receiver type is
  missing on a platform where the generated file builds (the type is
  declared only in `s_linux.go`, behind `//go:build`, or in a cgo file),
  listing each one and asking for a hand-written `DescX`. Before, it wrote
  the companion into `desc_gen.go`, which then failed to build on other
  platforms (`undefined: S`). Types split over complementary files, and
  `-o desc_linux.go` for a linux-only package, are accepted. `_x.go` and
  `.x.go` are no longer read, and a file that needs custom tags (such as
  `// +build ignore` or `integration`) contributes only its hand-written
  `DescX`, not task methods.
- `WhenHostnameIn` and `WhenHostnameContains` refuse an empty or
  whitespace-only hostname fragment as a declaration error. Before,
  `WhenHostnameIn("f0", "")` (or one fed an unset config value) recorded
  a guard every hostname matches, so the task applied on every
  destination. `WhenHostnameIn()` with no hosts no longer falls back to
  that match-all guard either.
- A `nil` TaskOption is a declaration error instead of a panic, since the
  missing option may have been a guard or `Privileged()`. In `Task(...)`
  the task is not registered; returned by an `OptsX()` companion, only
  that method is skipped; returned by `Opts()`, a `StructOption` marker or
  a struct's own `StructTaskOptions`, no task of the struct is
  registered. A nil marker is refused the same way instead of panicking:
  a nil pointer to a value-receiver marker such as an embedded
  `*RequiresRoot`, a `StructOption` field left unset, or a nil pointer
  deeper in the embedded chain (`struct{ Base }` with
  `Base struct{ *RequiresRoot }`). A nil pointer to a marker with a
  pointer-receiver `StructTaskOptions` is still called, as before, since
  that is legal Go and the method may handle a nil receiver. If that call
  panics with a nil pointer dereference (probably of its nil receiver),
  the panic is reported as a nil marker, fail-safe; every other panic
  propagates unchanged. Among `RegisterMethods` options
  (directly or inside `WithGroupWhen`) a nil TaskOption registers nothing
  of the struct, and in `RegisterOnCluster` nothing of any struct of the
  call.
- Behaviour change: a `nil` `RegisterOption` passed to `RegisterMethods`,
  or a `nil` item in `RegisterOnCluster` (untyped or a nil struct
  pointer), is a declaration error and registers nothing. Before,
  `RegisterMethods` skipped it silently, so a guard such as `OnCluster`
  built as nil registered the methods unguarded.
- A `StructOption` marker with a pointer-receiver `StructTaskOptions` is
  now a marker field wherever it is embedded: by value (`struct{ PM }`),
  by pointer (`struct{ *PM }`), or promoted by value through an exported
  embed (`struct{ Inner }` with `type Inner struct{ PM }`). Before, only
  fields whose own type (or pointed-to type) implemented `StructOption`
  counted, so next to another marker such a marker's options (such as a
  guard) were silently dropped and the tasks registered without them.
- Behaviour change: for the same three forms as a struct's only marker,
  its options now compose before the `Opts()` companion, like every
  other marker ("markers first, then `Opts()`"). Before, they were
  reached through the struct's promoted `StructTaskOptions` after
  `Opts()`, so a later-wins option flips: a marker's `Privileged()` with
  an `Opts()` returning `Unprivileged()` was privileged and is now
  unprivileged.
- A companion called during `RegisterMethods` (`Opts`, `OptsX`,
  `WhenX()`, `DescX`) promoted through a nil embedded pointer or
  interface (`struct{ *Base }` with `Base` nil and
  `func (Base) Opts() TaskOptions`) is a declaration error instead of a
  panic: `Opts` refuses the struct, the others skip that task. A task
  method or `WhenX(Facts)` predicate, which run later, is refused the
  same way only when the struct is registered by value and the nil embed
  is held in that copy itself (directly, or in a by-value embedded
  struct): it can never be set, so the task would panic when it runs. A
  nil embed behind a non-nil embedded pointer or interface is shared with
  the recipe, and a struct registered by pointer reads its embeds when
  the method runs, so either may still be set after `RegisterMethods`. A nil pointer to a
  pointer-receiver companion is still called, like a nil-safe marker.
- `RegisterMethods` no longer slows down exponentially on recursive
  embedded types (types embedding each other by pointer): the promotion
  chain of a marker or companion is found with a breadth-first search
  that visits each type once.
- Behaviour change: a struct that declares its own `StructTaskOptions`
  overrides its embedded markers, as in Go method resolution: only its
  own method's options apply. Before, a value-receiver marker next to it
  replaced the struct's own method, so `struct{ RequiresRoot }` with its
  own `StructTaskOptions` got `Privileged()` and lost its own options.
- `RegisterMethods` companion errors name the struct type:
  `RegisterMethods(pkg.Type): OptsX must be func() TaskOptions`.

Docs
- Gonfy the beaver is gonf's mascot and new logo (`assets/logo-*.svg`).
  The README, the reference and the tutorial are themed around him, and
  the tutorial's example recipes manage his things (a portrait file, a
  lodge motd, a `gonfy` account and cron job); their outputs were
  recaptured. The demo tasks in `examples/` write a Gonfy file too.
- A final tutorial chapter, "A web fleet", sets up Apache httpd on three
  Linux front ends with one recipe (`docs/tutorial/examples/ch16-web-fleet`),
  using the inventory, `ConfigSet` validation, secrets, per-host timers
  and change gates together.

## v0.24.0 (2026-09-26)

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
- A local run selects hosts by their `WithHostnameMatch` fragment, so their
  `EachHost`/`ForHosts` bodies apply on the machine the fragment matches.
- `Needs(T.Method)` resolves the exact task under the dependent's prefix
  when one registration's prefix starts with another's.
- `gonf-desc` writes one `DescX` for a method defined in per-GOOS files.
- `gonf push -- <ssh-opts>` keeps the value of every value-taking ssh
  option (`-E`, `-m`, `-B`, ...) instead of reading it as the host.
- `WithGonfPath` is shell-quoted in remote apply and install commands; the
  remote gonf-sync staging dir is removed after a cancelled push too.
- Security: the sticky `-apply-dir`, the apply staging root and blob tar
  extraction no longer follow a planted symlink; the staging root avoids a
  shared `gonf-apply` dir owned by another user.
- A symlinked `WithSource` directory is synced (it synced nothing and, with
  `WithPrune` on the plan path, emptied the destination).
- `EnsureDir` on the plan path leaves an existing directory alone.
- `WithBlock` refuses nested block markers; line edits handle lines over
  64 KiB.
- A dry run previews a `Symlink` whose target an earlier resource of the
  plan creates; relative symlink targets resolve through a symlinked
  parent like the kernel does.
- A secret quoted with `%q` in an error is redacted; redaction keeps the
  line break after a secret read with a trailing newline.
- `GitGlobal` applies an empty value and settles on one with surrounding
  whitespace.
- `IsLatest` installs a missing package on dnf and FreeBSD; NetBSD services
  use the `one*` verbs so disabled running daemons are handled;
  `NoService`/`NoTimer` settle on static, indirect, generated, alias and
  runtime-enabled systemd units; a relative `Creates` is resolved against
  `WithDir`.

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
